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
