package pier

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/usepier/pier/internal/config"
)

// Jobs are long work that runs as a detached pier process — a create, a
// bake, a pool fill, a resize — so it finishes whatever happens to the
// frontend that started it: a closed app, a crashed terminal. Each job
// writes its progress twice: the human log it always wrote, and an event
// file of JSON lines a frontend follows, ending in a done or failed event.
// A registry entry per job lets a frontend that restarts find its jobs
// again.

// Final events: a job's last word.
const (
	EventDone   EventKind = "done"   // the job finished; Message summarizes
	EventFailed EventKind = "failed" // the job failed; Message is why
)

// JobKind names what a job does.
type JobKind string

const (
	JobCreate  JobKind = "create"
	JobBake    JobKind = "bake"
	JobFill    JobKind = "fill"
	JobResize  JobKind = "resize"
	JobDeliver JobKind = "deliver" // wake a parked session and queue a prompt there
)

// JobState is where a job is.
type JobState string

const (
	JobRunning JobState = "running"
	JobDone    JobState = "done"
	JobFailed  JobState = "failed"
	JobLost    JobState = "lost" // the process is gone without a final event (killed)
)

// Job is one background job.
type Job struct {
	ID      string    `json:"id"`
	Kind    JobKind   `json:"kind"`
	Repo    string    `json:"repo,omitempty"`
	Session string    `json:"session,omitempty"`
	PID     int       `json:"pid"`
	Log     string    `json:"log"`    // the human log
	Events  string    `json:"events"` // JSON-lines event file
	Started time.Time `json:"started"`

	// Filled in by Jobs/JobByID from the event file and the process.
	State   JobState  `json:"-"`
	Error   string    `json:"-"` // a failed job's reason
	Summary string    `json:"-"` // a finished job's last word
	Ended   time.Time `json:"-"`
}

func jobsDir() string { return filepath.Join(config.Dir(), "jobs") }

// jobKeep is how long finished jobs stay listed.
const jobKeep = 24 * time.Hour

func newJobID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// startJob spawns `pier args...` detached, with an event file, and records
// it in the registry.
func startJob(kind JobKind, repo, session, dir, logPath string, appendLog bool, args ...string) (Job, error) {
	if err := os.MkdirAll(jobsDir(), 0o700); err != nil {
		return Job{}, err
	}
	j := Job{ID: newJobID(), Kind: kind, Repo: repo, Session: session, Log: logPath, Started: time.Now().UTC()}
	j.Events = filepath.Join(jobsDir(), j.ID+".events")
	pid, err := spawnDetachedPID(dir, logPath, appendLog, append(args, "--events", j.Events)...)
	if err != nil {
		return Job{}, err
	}
	j.PID = pid
	b, _ := json.Marshal(j)
	if err := os.WriteFile(filepath.Join(jobsDir(), j.ID+".json"), b, 0o600); err != nil {
		return Job{}, err
	}
	j.State = JobRunning
	return j, nil
}

// StartCreate runs a create in the background (the TUI's n, the app's New
// Session). The row lists as starting until the create marks it ready.
func (c *Client) StartCreate(req CreateRequest) (Job, error) {
	if req.RepoRoot == "" {
		return Job{}, ErrNotInRepo
	}
	args := []string{"new", req.Branch}
	if req.Base != "" {
		args = append(args, req.Base)
	}
	args = append(args, "--detach")
	if req.Idle != nil {
		args = append(args, "--idle", durFlag(*req.Idle))
	}
	if req.Cap != nil {
		args = append(args, "--cap", durFlag(*req.Cap))
	}
	if req.NoPool {
		args = append(args, "--no-pool")
	}
	if a := req.Agent; a != nil {
		args = append(args, "--agent", a.Kind)
		if a.Model != "" {
			args = append(args, "--model", a.Model)
		}
		if a.PermissionMode != "" {
			args = append(args, "--permission-mode", a.PermissionMode)
		}
		if a.Prompt != "" {
			// The prompt rides a private file, not argv: process lists are
			// readable by every user of the machine.
			if err := os.MkdirAll(jobsDir(), 0o700); err != nil {
				return Job{}, err
			}
			pf := filepath.Join(jobsDir(), "prompt-"+newJobID())
			if err := os.WriteFile(pf, []byte(a.Prompt), 0o600); err != nil {
				return Job{}, err
			}
			args = append(args, "--prompt-file", pf)
		}
	}
	return startJob(JobCreate, filepath.Base(req.RepoRoot), req.Branch, req.RepoRoot, CreateLogPath(req.Branch), false, args...)
}

func durFlag(d time.Duration) string {
	if d == 0 {
		return "never"
	}
	return d.String()
}

// StartBake runs `pier bake` for the repo at repoRoot in the background.
// include overrides the speed.image_repo setting for this bake (nil keeps it).
func (c *Client) StartBake(repoRoot string, include *bool) (Job, error) {
	repo := filepath.Base(repoRoot)
	if BakeRunning(repo) {
		return Job{}, fmt.Errorf("a bake of %s is already running", repo)
	}
	args := []string{"bake"}
	if include != nil {
		if *include {
			args = append(args, "--with-repo")
		} else {
			args = append(args, "--toolchain-only")
		}
	}
	j, err := startJob(JobBake, repo, "", repoRoot, BakeLogPath(repo), false, args...)
	if err != nil {
		return Job{}, err
	}
	_ = os.WriteFile(bakePIDPath(repo), []byte(fmt.Sprint(j.PID)), 0o600)
	return j, nil
}

// StartFill tops up the repo's pool in the background.
func (c *Client) StartFill(repoRoot string) (Job, error) {
	repo := filepath.Base(repoRoot)
	// Append, never truncate: a claim can spawn a refill while the previous
	// one is still writing the same log.
	return startJob(JobFill, repo, "", repoRoot, PoolLogPath(repo), true, "pool", "fill")
}

// StartResize resizes a session in the background: park, change type,
// resume. A resize interrupted halfway would leave the session parked, so
// it never runs inside a frontend's process.
func (c *Client) StartResize(s Session, machineType string) (Job, error) {
	if err := CheckReady(s); err != nil {
		return Job{}, err
	}
	logPath := filepath.Join(config.Dir(), "logs", "resize-"+sanitizeStem(s.Name)+".log")
	home, _ := os.UserHomeDir()
	return startJob(JobResize, s.Repo, s.Name, home, logPath, false, "resize", s.Name, machineType)
}

func sanitizeStem(name string) string { return strings.NewReplacer("/", "-", " ", "-").Replace(name) }

// Jobs lists the registry, newest first, with each job's state read from
// its event file and process. Finished jobs older than a day are pruned.
func Jobs() []Job {
	entries, _ := os.ReadDir(jobsDir())
	var out []Job
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		j, err := JobByID(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		if j.State != JobRunning && !j.Ended.IsZero() && time.Since(j.Ended) > jobKeep {
			removeJob(j)
			continue
		}
		out = append(out, j)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Started.After(out[b].Started) })
	return out
}

// JobByID reads one job and its state.
func JobByID(id string) (Job, error) {
	if id == "" || strings.ContainsAny(id, "/.") {
		return Job{}, ErrNotFound
	}
	b, err := os.ReadFile(filepath.Join(jobsDir(), id+".json"))
	if err != nil {
		return Job{}, fmt.Errorf("job %s: %w", id, ErrNotFound)
	}
	var j Job
	if err := json.Unmarshal(b, &j); err != nil {
		return Job{}, err
	}
	j.State = JobRunning
	events, _ := readEvents(j.Events, 0)
	for _, e := range events.list {
		switch e.Kind {
		case EventDone:
			j.State, j.Summary = JobDone, e.Message
		case EventFailed:
			j.State, j.Error = JobFailed, e.Message
		}
	}
	if j.State != JobRunning {
		if st, err := os.Stat(j.Events); err == nil {
			j.Ended = st.ModTime()
		}
	} else if !processAlive(j.PID) {
		j.State = JobLost
		j.Error = "the job's process ended without reporting how — see its log"
		if st, err := os.Stat(j.Log); err == nil {
			j.Ended = st.ModTime()
		}
	}
	return j, nil
}

func removeJob(j Job) {
	os.Remove(filepath.Join(jobsDir(), j.ID+".json"))
	os.Remove(j.Events)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// CancelJob stops a running job the way ctrl-c would: its process group
// gets SIGTERM, so cleanup (terminating a half-made instance) still runs.
func CancelJob(id string) error {
	j, err := JobByID(id)
	if err != nil {
		return err
	}
	if j.State != JobRunning {
		return nil
	}
	// The job leads its own process group (setsid): signal the group so
	// its ssh children go too.
	if err := syscall.Kill(-j.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

type eventBatch struct {
	list []Event
	off  int64
}

// readEvents reads whole event lines from off.
func readEvents(path string, off int64) (eventBatch, error) {
	f, err := os.Open(path)
	if err != nil {
		return eventBatch{off: off}, err
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return eventBatch{off: off}, err
	}
	r := bufio.NewReader(f)
	out := eventBatch{off: off}
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break // a partial last line waits for its newline
		}
		out.off += int64(len(line))
		var e Event
		if json.Unmarshal(line, &e) == nil {
			out.list = append(out.list, e)
		}
	}
	return out, nil
}

// JobEvents returns the events a job has written so far and the offset
// after them, for FollowJob to continue from.
func JobEvents(j Job) ([]Event, int64) {
	b, _ := readEvents(j.Events, 0)
	return b.list, b.off
}

// FollowJob calls fn with every event the job writes after offset from (0
// for all of them), in order, until its final event, until its process
// dies without one (fn then gets a failed event saying so), or until ctx
// ends.
func FollowJob(ctx context.Context, id string, from int64, fn func(Event)) error {
	j, err := JobByID(id)
	if err != nil {
		return err
	}
	off := from
	for {
		b, _ := readEvents(j.Events, off)
		off = b.off
		for _, e := range b.list {
			fn(e)
			if e.Kind == EventDone || e.Kind == EventFailed {
				return nil
			}
		}
		if !processAlive(j.PID) {
			// One more read: the process may have written its last line
			// between the read above and its exit.
			b, _ := readEvents(j.Events, off)
			for _, e := range b.list {
				fn(e)
				if e.Kind == EventDone || e.Kind == EventFailed {
					return nil
				}
			}
			fn(Event{Kind: EventFailed, Message: "the job's process ended without reporting how — see its log"})
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// EventLog is the writing end of a job's event file: the detached pier
// process a job runs as writes its progress here.
type EventLog struct {
	mu    sync.Mutex
	f     *os.File
	start time.Time
	ended bool
}

// OpenEventLog opens (appending) the event file a job was started with.
func OpenEventLog(path string) (*EventLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &EventLog{f: f, start: time.Now()}, nil
}

// Write records one event.
func (l *EventLog) Write(e Event) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return
	}
	if e.Elapsed == 0 {
		e.Elapsed = time.Since(l.start)
	}
	b, _ := json.Marshal(e)
	l.f.Write(append(b, '\n'))
}

// Finish writes the job's final event — done, or failed with err — once.
func (l *EventLog) Finish(err error, summary string) {
	if l == nil {
		return
	}
	if err != nil {
		l.Write(Event{Kind: EventFailed, Message: err.Error()})
	} else {
		l.Write(Event{Kind: EventDone, Message: summary})
	}
	l.mu.Lock()
	l.ended = true
	l.f.Close()
	l.mu.Unlock()
}
