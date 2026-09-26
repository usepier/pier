package pier

import (
	"errors"
	"time"
)

// EventKind classifies a progress event so frontends style it without
// parsing its text.
type EventKind string

const (
	EventStep EventKind = "step" // a phase of a long operation began
	EventNote EventKind = "note" // advice worth showing, not a failure
	EventWarn EventKind = "warn" // something degraded but the operation goes on
)

// Event is one line of progress from a long operation (create, bake, fill).
// Elapsed is measured from the operation's start, so every frontend can show
// which step ate the wait.
type Event struct {
	Kind    EventKind     `json:"kind"`
	Message string        `json:"message"`
	Elapsed time.Duration `json:"elapsed"`
}

// Progress receives events. A nil Progress discards them.
type Progress func(Event)

// stepper adapts a Progress to the func(string) the drivers and pool report
// through, stamping elapsed time from start.
func (p Progress) stepper(start time.Time) func(string) {
	return func(msg string) { p.emit(start, EventStep, msg) }
}

func (p Progress) emit(start time.Time, kind EventKind, msg string) {
	if p != nil {
		p(Event{Kind: kind, Message: msg, Elapsed: time.Since(start)})
	}
}

// Errors frontends branch on. Wrapped errors carry the detail; errors.Is
// recognizes the kind.
var (
	ErrNotInRepo     = errors.New("not inside a git repository")
	ErrNotFound      = errors.New("no such session")
	ErrAmbiguous     = errors.New("session name is ambiguous")
	ErrStillCreating = errors.New("session is still setting up")
	ErrCreateFailed  = errors.New("session never finished creating")
	ErrDeleting      = errors.New("session is being deleted")
	ErrParked        = errors.New("session is parked")
)
