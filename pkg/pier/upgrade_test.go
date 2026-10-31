package pier

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Every view of a session finding its supervisor stale at once installs it
// once; one installed moments ago isn't installed again while it starts.
func TestUpgradeOnce(t *testing.T) {
	supervisorStart = 10 * time.Millisecond
	c := &Client{}
	var installs atomic.Int32
	install := func() error {
		installs.Add(1)
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.upgradeOnce(context.Background(), "i-1", install); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := installs.Load(); n != 1 {
		t.Errorf("installed %d times", n)
	}
	// Another session is its own.
	_ = c.upgradeOnce(context.Background(), "i-2", install)
	if n := installs.Load(); n != 2 {
		t.Errorf("installed %d times", n)
	}
}
