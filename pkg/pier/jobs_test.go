package pier

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/usepier/pier/internal/config"
)

func writeJob(t *testing.T, j Job) {
	t.Helper()
	os.MkdirAll(jobsDir(), 0o700)
	b, _ := json.Marshal(j)
	if err := os.WriteFile(filepath.Join(jobsDir(), j.ID+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A job's state comes from its event file first and its process second: a
// final event decides; a dead process without one is lost, never running.
func TestJobState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	live := exec.Command("sleep", "30")
	live.Start()
	defer live.Process.Kill()
	dead := exec.Command("true")
	dead.Run()

	mk := func(id string, pid int) Job {
		j := Job{ID: id, Kind: JobFill, PID: pid, Events: filepath.Join(jobsDir(), id+".events"),
			Log: filepath.Join(config.Dir(), "x.log"), Started: time.Now()}
		writeJob(t, j)
		return j
	}
	run := mk("aaa111", live.Process.Pid)
	l, _ := OpenEventLog(run.Events)
	l.Write(Event{Kind: EventStep, Message: "filling"})
	if j, _ := JobByID("aaa111"); j.State != JobRunning {
		t.Errorf("a live job with no final event runs, got %s", j.State)
	}
	l.Finish(errors.New("quota"), "")
	if j, _ := JobByID("aaa111"); j.State != JobFailed || j.Error != "quota" {
		t.Errorf("failed job = %s %q", j.State, j.Error)
	}
	mk("bbb222", dead.Process.Pid)
	if j, _ := JobByID("bbb222"); j.State != JobLost {
		t.Errorf("a dead job without a final event is lost, got %s", j.State)
	}
	if _, err := JobByID("../etc"); !errors.Is(err, ErrNotFound) {
		t.Error("ids never reach outside the registry")
	}
	if got := Jobs(); len(got) != 2 {
		t.Errorf("Jobs = %d entries", len(got))
	}
}

func TestFollowJobFromAnOffset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	live := exec.Command("sleep", "30")
	live.Start()
	defer live.Process.Kill()
	j := Job{ID: "ccc333", PID: live.Process.Pid, Events: filepath.Join(jobsDir(), "ccc333.events"), Started: time.Now()}
	writeJob(t, j)
	l, _ := OpenEventLog(j.Events)
	l.Write(Event{Kind: EventStep, Message: "one"})
	_, off := JobEvents(j)
	go func() {
		time.Sleep(100 * time.Millisecond)
		l.Write(Event{Kind: EventStep, Message: "two"})
		l.Finish(nil, "ok")
	}()
	var got []string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := FollowJob(ctx, j.ID, off, func(e Event) { got = append(got, e.Message) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "two" || got[1] != "ok" {
		t.Errorf("followed %v, want [two ok]", got)
	}
}

// A job that dies without a final event still ends the follow, as failed.
func TestFollowJobEndsWhenTheProcessDies(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := exec.Command("sleep", "0.3")
	p.Start()
	go p.Wait()
	j := Job{ID: "ddd444", PID: p.Process.Pid, Events: filepath.Join(jobsDir(), "ddd444.events"), Started: time.Now()}
	writeJob(t, j)
	var last Event
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	FollowJob(ctx, j.ID, 0, func(e Event) { last = e })
	if last.Kind != EventFailed {
		t.Errorf("last event = %+v, want failed", last)
	}
}

// A bake hands its job to its background finish: the job runs while the
// finish does — though the bake's own process has ended — and ends with
// what the finish reports, in the same events.
func TestJobHandedOffRunsUntilItsFinishEnds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bake := exec.Command("true")
	bake.Run()
	finish := exec.Command("sleep", "30")
	finish.Start()
	defer finish.Process.Kill()

	j := Job{ID: "bak111", Kind: JobBake, PID: bake.Process.Pid, Events: filepath.Join(jobsDir(), "bak111.events"),
		Log: filepath.Join(config.Dir(), "x.log"), Started: time.Now()}
	writeJob(t, j)
	l, _ := OpenEventLog(j.Events)
	l.Write(Event{Kind: EventStep, Message: "imaging"})
	if err := handOffJob(l.Path(), finish.Process.Pid); err != nil {
		t.Fatal(err)
	}
	l.Handoff()
	l.Finish(nil, "the bake's process mustn't end it") // after a handoff, nothing
	if got, _ := JobByID("bak111"); got.State != JobRunning {
		t.Fatalf("handed off = %s", got.State)
	}
	fl, _ := OpenEventLog(j.Events)
	fl.Write(Event{Kind: EventStep, Message: "snapshot 40%"})
	fl.Finish(nil, "image ready")
	got, _ := JobByID("bak111")
	events, _ := JobEvents(got)
	if got.State != JobDone || got.Summary != "image ready" || len(events) != 3 {
		t.Errorf("job = %s %q, %d events", got.State, got.Summary, len(events))
	}
}

// Following a job that's handed off mid-way goes on with the process it's
// handed to, to its final event — not ending with the first one's exit.
func TestFollowJobAcrossAHandoff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bake := exec.Command("sleep", "0.6")
	bake.Start()
	go bake.Wait()
	finish := exec.Command("sleep", "30")
	finish.Start()
	defer finish.Process.Kill()
	j := Job{ID: "eee555", Kind: JobBake, PID: bake.Process.Pid, Events: filepath.Join(jobsDir(), "eee555.events"), Started: time.Now()}
	writeJob(t, j)
	l, _ := OpenEventLog(j.Events)
	l.Write(Event{Kind: EventStep, Message: "imaging"})
	go func() {
		// Followed from the start; handed off while followed.
		time.Sleep(200 * time.Millisecond)
		handOffJob(l.Path(), finish.Process.Pid)
		l.Handoff()
		time.Sleep(1200 * time.Millisecond) // the bake's process is gone by now
		fl, _ := OpenEventLog(j.Events)
		fl.Write(Event{Kind: EventStep, Message: "snapshot 100%"})
		fl.Finish(nil, "image ready")
	}()
	var got []Event
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	FollowJob(ctx, j.ID, 0, func(e Event) { got = append(got, e) })
	if len(got) != 3 || got[2].Kind != EventDone || got[2].Message != "image ready" {
		t.Errorf("followed = %+v", got)
	}
}
