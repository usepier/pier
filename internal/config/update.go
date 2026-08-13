package config

// update.go: safe concurrent writes.
//
// The config is one file rewritten whole on every save, and pier is many
// short-lived processes plus a long-lived TUI. Load-then-save therefore loses
// updates: a process that read the file a minute ago writes back what it read,
// erasing anything recorded in between. The casualty that hurts is the
// baked-image map, because losing it is silent — the next create launches from
// the stock image, and the repo's setup script fails minutes later on a
// toolchain that should have been baked in, with nothing connecting the two
// events. A bake finishing while a settings page is open, or two `pier bake`
// runs for different repos overlapping, is all it takes.
//
// Update is the fix and the rule: never save a Config that has been sitting in
// memory. Take the lock, re-read, mutate, write.

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockPath is a sidecar, not the config itself: Save swaps the config in by
// rename, so a lock held on that inode would stop guarding anything.
func lockPath() string { return Path() + ".lock" }

// lockTimeout bounds the wait. The critical section is a read, a struct
// mutation and a rename — microseconds — so anything approaching this is a
// wedged or crashed process, and blocking a `pier bake` forever on it would
// be worse than failing with a message that says where to look.
const lockTimeout = 5 * time.Second

// lockConfig takes the exclusive cross-process lock. flock releases on process
// exit, so a killed pier leaves nothing stale behind.
func lockConfig() (func(), error) {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}, nil
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, fmt.Errorf("config busy: another pier process has held %s for %s", lockPath(), lockTimeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Update re-reads the config under an exclusive lock, applies mutate, and
// writes the result — so a change lands on top of whatever else has been
// written since this process last looked, instead of reverting it. It returns
// the saved config so callers can refresh their copy.
//
// Every write that changes one part of the config should go through here.
// Config.Save is for callers that genuinely own the whole file (the setup
// wizard, which has just asked about all of it).
func Update(mutate func(*Config) error) (Config, error) {
	unlock, err := lockConfig()
	if err != nil {
		return Config{}, err
	}
	defer unlock()

	c := Default()
	if _, err := os.Stat(Path()); err == nil {
		loaded, err := Load()
		if err != nil {
			// Refuse rather than reset: overwriting a config we couldn't parse
			// is how a whole account's setup disappears.
			return Config{}, fmt.Errorf("%w — fix or remove %s (previous version: %s)",
				err, Path(), Path()+".bak")
		}
		c = loaded
	}
	if err := mutate(&c); err != nil {
		return Config{}, err
	}
	if err := c.write(); err != nil {
		return Config{}, err
	}
	return c, nil
}
