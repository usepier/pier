package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Parking is a shutdown, and a shutdown stops every container. A laptop gets
// its stack back after a reboot because the containers still exist and come
// back up; a woken session gets the same here: the supervisor keeps the list
// of containers that were running (with the tmux snapshot, and once more
// right before it parks), and the first supervisor of each boot starts those
// exact containers again — no compose file, no repo script, nothing to
// configure. Setup never re-runs: the containers, their volumes and their
// data are all still on the disk.

// containersFile holds the IDs of the containers running at the last
// snapshot. Kept outside ~/.pier/tmux: a claim discards the saved tmux
// layout, but its ready session's containers are exactly what it wants back.
func containersFile(home string) string { return filepath.Join(home, ".pier", "containers.json") }

// containersRestarted marks, per boot (/run is tmpfs), that this boot's
// restart already ran, so a supervisor restart doesn't repeat it.
var containersRestarted = "/run/pier/containers-restarted"

// restartedThisBoot is set once the boot's restart has run (or there was
// nothing to restart). Until then snapshots leave the saved list alone: a
// snapshot taken while docker is still starting would record nothing and
// lose the list the restart is about to use.
var restartedThisBoot atomic.Bool

// docker runs the docker CLI; a var so tests can stand in a daemon.
var docker = func(args ...string) ([]byte, error) {
	return exec.Command("docker", args...).Output()
}

// snapshotContainers records the running containers. A docker that doesn't
// answer leaves the previous list in place.
func snapshotContainers(home string) {
	if !restartedThisBoot.Load() {
		return
	}
	out, err := docker("ps", "-q", "--no-trunc")
	if err != nil {
		return
	}
	ids := strings.Fields(string(out))
	if ids == nil {
		ids = []string{}
	}
	b, _ := json.Marshal(ids)
	_ = os.MkdirAll(filepath.Dir(containersFile(home)), 0o700)
	_ = writeIfChanged(containersFile(home), b)
}

// restartContainers starts the containers that were running when the
// session last parked. It waits for the docker daemon (it starts alongside
// the supervisor at boot) for up to wait, then starts them in one call —
// docker orders nothing, but compose healthchecks and restart policies still
// apply to each container as it comes up. Returns a line for the journal.
func restartContainers(home string, wait time.Duration) string {
	defer restartedThisBoot.Store(true)
	if _, err := os.Stat(containersRestarted); err == nil {
		return ""
	}
	defer func() {
		_ = os.MkdirAll(filepath.Dir(containersRestarted), 0o755)
		_ = os.WriteFile(containersRestarted, nil, 0o644)
	}()
	b, err := os.ReadFile(containersFile(home))
	if err != nil {
		return ""
	}
	var ids []string
	if json.Unmarshal(b, &ids) != nil || len(ids) == 0 {
		return ""
	}
	deadline := time.Now().Add(wait)
	for {
		if _, err := docker("info", "--format", "{{.ID}}"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Sprintf("containers: docker didn't answer within %s — %d container(s) not restarted", wait, len(ids))
		}
		time.Sleep(time.Second)
	}
	// Containers removed since the snapshot fail individually; docker still
	// starts the rest.
	out, err := docker(append([]string{"start"}, ids...)...)
	started := len(strings.Fields(string(out)))
	if err != nil {
		return fmt.Sprintf("containers: restarted %d of %d (%v)", started, len(ids), err)
	}
	return fmt.Sprintf("containers: restarted %d that were running before the park", started)
}
