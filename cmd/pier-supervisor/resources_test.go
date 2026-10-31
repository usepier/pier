package main

import (
	"math"
	"testing"
)

func TestCPUSampler(t *testing.T) {
	var c cpuSampler
	if got := c.sample("cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 1 1 1 1 1 0 0 0 0 0\n"); got != 0 {
		t.Errorf("the first sample has no interval: %v", got)
	}
	// +50 user, +50 system, +100 idle: 100 busy of 200.
	if got := c.sample("cpu  150 0 150 800 100 0 0 0 0 0\n"); math.Abs(got-50) > 0.01 {
		t.Errorf("busy = %v, want 50", got)
	}
}

func TestParseMeminfo(t *testing.T) {
	used, total := parseMeminfo("MemTotal:        4000000 kB\nMemFree:          100000 kB\nMemAvailable:    1000000 kB\n")
	if total != 4000000*1024 || used != 3000000*1024 {
		t.Errorf("used/total = %d/%d", used, total)
	}
}

func TestProcTimes(t *testing.T) {
	// A command name with spaces and a paren must not shift the fields.
	stat := "1234 (node (dev) srv) S 1 1234 1234 0 -1 4194560 100 0 0 0 250 50 0 0 20 0 11 0 5000 100000000 2500 18446744073709551615"
	ticks, rss, ok := procTimes(stat)
	if !ok || ticks != 300 || rss != 2500 {
		t.Errorf("ticks/rss = %d/%d ok=%v", ticks, rss, ok)
	}
}

func TestPortOwners(t *testing.T) {
	out := `LISTEN 0 511 *:3000 *:* users:(("node",pid=1234,fd=18))
LISTEN 0 511 [::]:3000 [::]:* users:(("node",pid=1234,fd=19))
LISTEN 0 4096 0.0.0.0:5432 0.0.0.0:* users:(("docker-proxy",pid=990,fd=4))
LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=708,fd=3))
`
	got := portOwners(out, []int{3000, 5432})
	if len(got) != 2 || got[0] != (port{3000, "node", 1234}) || got[1] != (port{5432, "docker-proxy", 990}) {
		t.Errorf("owners = %+v", got)
	}
}
