package piercore

// inspect.go: what a running session is doing, in one round trip — the
// supervisor's beacon (/run/pier/status.json) and tmux's windows.

import (
	"encoding/json"
	"strings"
	"time"
)

const inspectSep = "---PIER-WINDOWS---"

func inspectScript() string {
	return "cat /run/pier/status.json 2>/dev/null || printf '{}'\n" +
		"printf '\\n" + inspectSep + "\\n'\n" +
		"tmux list-windows -t main -F " + shellQuote(windowFormat) + " 2>/dev/null || true"
}

// beacon is the part of the supervisor's status the phone shows (pkg/pier's
// beacon). Fields older supervisors don't write stay zero.
type beacon struct {
	State         string `json:"state"`
	Bootstrapping bool   `json:"bootstrapping"`
	Strained      bool   `json:"strained"`
	Setup         string `json:"setup"`
	Ports         []port `json:"ports"`
	Res           *struct {
		CPU      float64 `json:"cpu"`
		MemUsed  uint64  `json:"mem_used"`
		MemTotal uint64  `json:"mem_total"`
	} `json:"res"`
	ParkAt  *time.Time `json:"park_at"`
	CapAt   *time.Time `json:"cap_at"`
	Chats   []chat     `json:"chats"`
	Renamed string     `json:"renamed"`
	Branch  string     `json:"branch"`
}

func parseInspection(s session, out string) snapshot {
	status, windows, _ := strings.Cut(out, inspectSep)
	var b beacon
	_ = json.Unmarshal([]byte(strings.TrimSpace(status)), &b)
	if b.State == "working" || b.State == "idle" {
		s.State = b.State
	}
	// The checkout's own branch wins, then the name the session took after
	// its first prompt (pkg/pier's beacon.apply).
	if b.Branch != "" {
		s.Branch = b.Branch
	} else if b.Renamed != "" {
		s.Branch = b.Renamed
	}
	snap := snapshot{
		Session: s, Windows: parseWindows(windows), Ports: b.Ports, Chats: b.Chats,
		Strained: b.Strained, Setup: b.Setup, ParkAt: b.ParkAt, CapAt: b.CapAt,
	}
	if b.Res != nil {
		snap.CPU = b.Res.CPU
		if b.Res.MemTotal > 0 {
			snap.Memory = float64(b.Res.MemUsed) / float64(b.Res.MemTotal) * 100
		}
	}
	if snap.Ports == nil {
		snap.Ports = []port{}
	}
	if snap.Chats == nil {
		snap.Chats = []chat{}
	}
	return snap
}
