package piercore

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
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

func TestProjectsFromInstancesUsesAWSLocation(t *testing.T) {
	got := projectsFromInstances([]instance{{Repo: "c1", Repository: "under-construction"}})
	if len(got) != 1 {
		t.Fatalf("projectsFromInstances() returned %d projects", len(got))
	}
	if got[0].Repository != "under-construction" || got[0].Name != "c1" || got[0].Host != "AWS" {
		t.Fatalf("project = %#v, want under-construction · c1 · AWS", got[0])
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

func TestDialSSHTimesOutWhenServerNeverSendsBanner(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- connection
		}
	}()

	started := time.Now()
	_, err = dialSSH(context.Background(), listener.Addr().String(), &ssh.ClientConfig{
		User:            "agent",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}, 100*time.Millisecond)
	if err == nil {
		t.Fatal("dialSSH unexpectedly completed an SSH handshake")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("SSH banner timeout took %s", elapsed)
	}
	select {
	case connection := <-accepted:
		_ = connection.Close()
	default:
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

func TestSignOutClearsMobileSession(t *testing.T) {
	client, err := NewClient("")
	if err != nil {
		t.Fatal(err)
	}
	client.session = sessionState{
		AccessToken: "access-token",
		AccountID:   "123456789012",
		RoleName:    "Developer",
	}
	client.pending = &pendingAuthorization{ClientID: "pending-client"}
	client.instances["i-123"] = remoteInstance{Model: instance{ID: "i-123"}}

	if err := client.SignOut(); err != nil {
		t.Fatal(err)
	}
	if client.pending != nil || len(client.instances) != 0 || len(client.remotes) != 0 {
		t.Fatalf("sign-out left cached state: %#v", client)
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
		t.Fatal("signed-out mobile client is still configured")
	}
}
