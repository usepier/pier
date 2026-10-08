package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/pkg/pier"
)

func TestCreateStartsAJob(t *testing.T) {
	f := &fake{}
	c := start(t, f)
	repo := t.TempDir()
	exec.Command("git", "init", "-q", repo).Run()
	idle := int64(0)
	resp, _ := c.call("sessions.create", map[string]any{"path": repo, "branch": "feat/x", "idleMs": idle, "noPool": true})
	job, _ := resp["result"].(map[string]any)
	if job == nil || job["id"] != "j1" || job["kind"] != "create" || job["state"] != "running" {
		t.Fatalf("create = %v", resp)
	}
	f.mu.Lock()
	req := f.created[0]
	f.mu.Unlock()
	if req.Branch != "feat/x" || !req.NoPool || req.Idle == nil || *req.Idle != 0 || req.Cap != nil {
		t.Errorf("create request = %+v", req)
	}
	resp, _ = c.call("sessions.create", map[string]any{"path": repo, "branch": " "})
	if errKind(resp) != "invalid_params" {
		t.Errorf("a blank branch must be rejected: %v", resp)
	}
}

// A job watch hands over everything written so far, then streams what the
// job writes next, ending with its final event.
func TestJobWatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(config.Dir(), "jobs")
	os.MkdirAll(dir, 0o700)
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer sleeper.Process.Kill()
	j := pier.Job{ID: "abc123", Kind: pier.JobBake, Repo: "app", PID: sleeper.Process.Pid,
		Log: filepath.Join(dir, "x.log"), Events: filepath.Join(dir, "abc123.events"), Started: time.Now()}
	b, _ := json.Marshal(j)
	os.WriteFile(filepath.Join(dir, "abc123.json"), b, 0o600)
	log, err := pier.OpenEventLog(j.Events)
	if err != nil {
		t.Fatal(err)
	}
	log.Write(pier.Event{Kind: pier.EventStep, Message: "launching"})

	c := start(t, &fake{})
	resp, _ := c.call("job.watch", map[string]any{"id": "abc123"})
	snap := resp["result"].(map[string]any)["snapshot"].(map[string]any)
	if evs := snap["events"].([]any); len(evs) != 1 || snap["job"].(map[string]any)["state"] != "running" {
		t.Fatalf("snapshot = %v", snap)
	}
	log.Write(pier.Event{Kind: pier.EventStep, Message: "imaging"})
	ev := c.next("sub.event")["params"].(map[string]any)["data"].(map[string]any)
	if ev["events"].([]any)[0].(map[string]any)["message"] != "imaging" {
		t.Errorf("update = %v", ev)
	}
	log.Finish(nil, "baked")
	ev = c.next("sub.event")["params"].(map[string]any)["data"].(map[string]any)
	if ev["job"].(map[string]any)["state"] != "done" || ev["job"].(map[string]any)["summary"] != "baked" {
		t.Errorf("final update = %v", ev)
	}

	resp, _ = c.call("jobs.list", nil)
	if jobs := resp["result"].([]any); len(jobs) != 1 || jobs[0].(map[string]any)["state"] != "done" {
		t.Errorf("jobs.list = %v", resp["result"])
	}
}
