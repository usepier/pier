package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// resources is the session's load, as frontends show it: how busy the CPUs
// are, how much memory and disk is used. Sizes are bytes.
type resources struct {
	CPU       float64 `json:"cpu"` // % of all CPUs, over the last tick
	CPUs      int     `json:"cpus"`
	MemUsed   uint64  `json:"mem_used"`
	MemTotal  uint64  `json:"mem_total"`
	DiskUsed  uint64  `json:"disk_used"`
	DiskTotal uint64  `json:"disk_total"`
	Load1     float64 `json:"load1"`
}

// cpuSampler turns /proc/stat's cumulative jiffies into a busy percentage
// between two readings.
type cpuSampler struct {
	prevBusy, prevTotal uint64
}

func (c *cpuSampler) sample(stat string) float64 {
	busy, total := parseCPUStat(stat)
	db, dt := busy-c.prevBusy, total-c.prevTotal
	first := c.prevTotal == 0
	c.prevBusy, c.prevTotal = busy, total
	if first || dt == 0 || busy < db {
		return 0
	}
	return float64(db) / float64(dt) * 100
}

// parseCPUStat reads the aggregate "cpu" line: busy is everything but idle
// and iowait.
func parseCPUStat(stat string) (busy, total uint64) {
	for _, line := range strings.Split(stat, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		for i, v := range f[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			total += n
			if i != 3 && i != 4 { // idle, iowait
				busy += n
			}
		}
		return busy, total
	}
	return 0, 0
}

// parseMeminfo reads MemTotal and MemAvailable (kB) as bytes used/total.
func parseMeminfo(s string) (used, total uint64) {
	var avail uint64
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = n * 1024
		case "MemAvailable:":
			avail = n * 1024
		}
	}
	if avail > total {
		return 0, total
	}
	return total - avail, total
}

func readResources(cpu *cpuSampler) resources {
	var r resources
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		r.CPU = cpu.sample(string(b))
		r.CPUs = strings.Count(string(b), "\ncpu") // per-CPU lines follow the aggregate
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		r.MemUsed, r.MemTotal = parseMeminfo(string(b))
	}
	var fs syscall.Statfs_t
	if syscall.Statfs("/", &fs) == nil {
		r.DiskTotal = fs.Blocks * uint64(fs.Bsize)
		r.DiskUsed = (fs.Blocks - fs.Bfree) * uint64(fs.Bsize)
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			r.Load1, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	return r
}

// proc is one process as the environment view lists it.
type proc struct {
	PID     int     `json:"pid"`
	Command string  `json:"command"` // its command line, abbreviated
	CPU     float64 `json:"cpu"`     // % of one CPU over the sample
	RSS     uint64  `json:"rss"`     // bytes
}

// procTimes is utime+stime (ticks) and rss (pages) from /proc/<pid>/stat.
func procTimes(stat string) (ticks uint64, rssPages uint64, ok bool) {
	// The command name (field 2) may hold spaces and parens: fields after
	// the last ')' are fixed.
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, 0, false
	}
	f := strings.Fields(stat[i+1:])
	// After ')': state(3) ppid(4) … utime(14) stime(15) … rss(24).
	if len(f) < 22 {
		return 0, 0, false
	}
	ut, _ := strconv.ParseUint(f[11], 10, 64)
	st, _ := strconv.ParseUint(f[12], 10, 64)
	rss, _ := strconv.ParseUint(f[21], 10, 64)
	return ut + st, rss, true
}

func readProcTimes() map[int][2]uint64 {
	out := map[int][2]uint64{}
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		if t, rss, ok := procTimes(string(b)); ok {
			out[pid] = [2]uint64{t, rss}
		}
	}
	return out
}

func cmdline(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(b) == 0 {
		return ""
	}
	s := strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
	if len(s) > 160 {
		s = s[:157] + "…"
	}
	return s
}

// topProcesses samples every process twice, gap apart.
func topProcesses(n int, gap time.Duration) []proc {
	var s procSampler
	s.top(n)
	time.Sleep(gap)
	return s.top(n)
}

// procSampler keeps the previous reading so each sample's CPU is the use
// since the last one, without sleeping.
type procSampler struct {
	prev map[int][2]uint64
	at   time.Time
}

// top returns the n heaviest processes by CPU since the last call, plus the
// n heaviest by memory not already listed.
func (s *procSampler) top(n int) []proc {
	const clkTck = 100
	pageSize := uint64(os.Getpagesize())
	now, at := readProcTimes(), time.Now()
	gap := at.Sub(s.at).Seconds()
	var all []proc
	for pid, cur := range now {
		cpu := 0.0
		if before, ok := s.prev[pid]; ok && gap > 0 && cur[0] >= before[0] {
			cpu = float64(cur[0]-before[0]) / clkTck / gap * 100
		}
		all = append(all, proc{PID: pid, CPU: cpu, RSS: cur[1] * pageSize})
	}
	s.prev, s.at = now, at
	pick := map[int]bool{}
	var out []proc
	sort.Slice(all, func(i, j int) bool { return all[i].CPU > all[j].CPU })
	for _, p := range all[:min(n, len(all))] {
		pick[p.PID] = true
		out = append(out, p)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].RSS > all[j].RSS })
	for _, p := range all[:min(n, len(all))] {
		if !pick[p.PID] {
			out = append(out, p)
		}
	}
	for i := range out {
		out[i].Command = cmdline(out[i].PID)
	}
	return out
}

// psMain prints the heaviest processes as JSON: `pier-supervisor ps`.
func psMain() int {
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	if err := json.NewEncoder(w).Encode(topProcesses(8, 500*time.Millisecond)); err != nil {
		return 1
	}
	return 0
}
