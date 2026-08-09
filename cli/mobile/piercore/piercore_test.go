package piercore

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizedCallerARN(t *testing.T) {
	input := "arn:aws:sts::123456789012:assumed-role/Developer/alice@example.com"
	want := "arn:aws:sts::123456789012:assumed-role/Developer"
	if got := normalizedCallerARN(input); got != want {
		t.Fatalf("normalizedCallerARN() = %q, want %q", got, want)
	}
}

func TestParseInspection(t *testing.T) {
	output := `{"state":"working","listening":[3000,22],"strained":true,"setup":"complete"}
---PIER-APP-TABS---
@1` + tmuxSeparator + `shell` + tmuxSeparator + `1` + tmuxSeparator + `2` + tmuxSeparator + `zsh` + tmuxSeparator + `/home/agent/work/pier`
	got, err := parseInspection(instance{ID: "i-123", State: "running"}, output)
	if err != nil {
		t.Fatal(err)
	}
	if got.Instance.State != "working" || !got.Instance.Strained || got.Instance.Setup != "complete" {
		t.Fatalf("instance status was not merged: %#v", got.Instance)
	}
	if len(got.Tabs) != 1 || got.Tabs[0].ID != "@1" || got.Tabs[0].Panes != 2 {
		t.Fatalf("tabs = %#v", got.Tabs)
	}
	if len(got.Ports) != 2 || !got.Ports[0].IsHTTP || got.Ports[1].IsHTTP {
		t.Fatalf("ports = %#v", got.Ports)
	}
}

func TestTmuxSeparatorMatchesSerializedFormatOutput(t *testing.T) {
	if tmuxSeparator != `\037` {
		t.Fatalf("tmuxSeparator = %q, want tmux's serialized delimiter", tmuxSeparator)
	}
	wireRow := `@10\037shell\0371\0371\037bash\037/home/agent/work/pier`
	fields := strings.Split(wireRow, tmuxSeparator)
	if len(fields) != 6 {
		t.Fatalf("serialized tmux row split into %d fields", len(fields))
	}
}

func TestFreshClientIsNotConfigured(t *testing.T) {
	client, err := NewClient("")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := client.SetupStatus()
	if err != nil {
		t.Fatal(err)
	}
	var got setupStatus
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.Configured {
		t.Fatal("fresh mobile client unexpectedly configured")
	}
	if got.CLIVersion != "Embedded PierCore" {
		t.Fatalf("CLI version = %q", got.CLIVersion)
	}
}
