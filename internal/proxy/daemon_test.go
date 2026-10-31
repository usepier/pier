package proxy

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// One proxy answers; another start finds it, followers see its state, and
// a stop ends it.
func TestControlSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pierproxy") // a short path: sockets have a limit
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ran := make(chan error, 1)
	go func() {
		ran <- serveCtl(context.Background(), dir, func(ctx context.Context, onChange func(Status)) error {
			onChange(Status{Sessions: []SessionStatus{{ID: "i-1", Name: "fix"}}})
			<-ctx.Done()
			return nil
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !Running(dir) {
		if time.Now().After(deadline) {
			t.Fatal("the proxy never answered")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := serveCtl(context.Background(), dir, nil); !errors.Is(err, ErrRunning) {
		t.Errorf("a second proxy = %v", err)
	}
	got := make(chan Status, 1)
	ctx, cancel := context.WithCancel(context.Background())
	followed := make(chan error, 1)
	go func() { followed <- Follow(ctx, dir, func(st Status) { got <- st }) }()
	select {
	case st := <-got:
		if len(st.Sessions) != 1 || st.Sessions[0].Name != "fix" {
			t.Errorf("state = %+v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no state")
	}
	if err := Stop(dir); err != nil {
		t.Fatal(err)
	}
	if err := <-ran; err != nil {
		t.Errorf("serve = %v", err)
	}
	if err := <-followed; err != nil {
		t.Errorf("a follower of a proxy that stopped = %v", err)
	}
	cancel()
	if Running(dir) {
		t.Error("still answering after a stop")
	}
	if err := Stop(dir); !errors.Is(err, ErrNotRunning) {
		t.Errorf("stopping nothing = %v", err)
	}
}
