package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/usepier/pier/pkg/pier/wire"
)

// lockedBuffer is what a client reads, safe to look at while written.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) events() []wire.SubEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []wire.SubEvent
	for _, line := range strings.Split(strings.TrimSpace(l.b.String()), "\n") {
		var m struct{ Params wire.SubEvent }
		if json.Unmarshal([]byte(line), &m) == nil && m.Params.Sub != "" {
			out = append(out, m.Params)
		}
	}
	return out
}

func subUnder(t *testing.T) (*Sub[int], *lockedBuffer, context.CancelFunc) {
	buf := &lockedBuffer{}
	s := &Server{out: bufio.NewWriter(buf)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return newSub[int](s, "t-1", "t", ctx), buf, cancel
}

func waitEvents(t *testing.T, buf *lockedBuffer, n int) []wire.SubEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		evs := buf.events()
		if len(evs) >= n || time.Now().After(deadline) {
			return evs
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Updates go out in order, only once the watch reply has, and emitting
// never waits on the client.
func TestSubQueuesInOrder(t *testing.T) {
	u, buf, _ := subUnder(t)
	for i := range 100 {
		u.Emit(i)
	}
	time.Sleep(20 * time.Millisecond)
	if len(buf.events()) != 0 {
		t.Fatal("an update went out before the watch reply")
	}
	close(u.opened)
	evs := waitEvents(t, buf, 100)
	if len(evs) != 100 {
		t.Fatalf("%d events", len(evs))
	}
	for i, ev := range evs {
		if ev.Seq != i+1 || ev.Data != float64(i) {
			t.Fatalf("event %d = %+v", i, ev)
		}
	}
}

// A client too far behind is told to start over: one event that skips a
// sequence number, then nothing.
func TestSubOverflowAsksToStartOver(t *testing.T) {
	u, buf, _ := subUnder(t)
	for i := range maxQueued + 10 {
		u.Emit(i)
	}
	close(u.opened)
	waitEvents(t, buf, 1)
	u.Emit(1)
	time.Sleep(20 * time.Millisecond)
	evs := buf.events()
	if len(evs) != 1 || evs[0].Seq != 2 || evs[0].Data != nil {
		t.Fatalf("events = %+v", evs)
	}
}
