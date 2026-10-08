package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// The protocol owns stdout: once pier api takes it, prints from pier or its
// children land on stderr and only protocol writes reach stdout.
func TestTakeStdoutGuardsTheProtocol(t *testing.T) {
	if os.Getenv("PIER_STDOUT_HELPER") == "1" {
		proto, err := takeStdout()
		if err != nil {
			os.Exit(2)
		}
		fmt.Println("stray print")
		c := exec.Command("echo", "stray child")
		c.Stdout = os.Stdout
		c.Run()
		proto.WriteString("{\"jsonrpc\":\"2.0\"}\n")
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestTakeStdoutGuardsTheProtocol")
	cmd.Env = append(os.Environ(), "PIER_STDOUT_HELPER=1")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper: %v\n%s", err, errb.String())
	}
	if out.String() != "{\"jsonrpc\":\"2.0\"}\n" {
		t.Errorf("stdout = %q, want only the protocol line", out.String())
	}
	if !bytes.Contains(errb.Bytes(), []byte("stray print")) || !bytes.Contains(errb.Bytes(), []byte("stray child")) {
		t.Errorf("stray output must reach stderr, got %q", errb.String())
	}
}

// A background job's hidden --events flag is consumed before dispatch and
// opens the job's event file, wherever it sits on the command line.
func TestTakeEventsFlag(t *testing.T) {
	path := t.TempDir() + "/j.events"
	defer func() { jobEvents = nil }()
	got := takeEventsFlag([]string{"new", "feat", "--events", path, "--detach"})
	if len(got) != 3 || got[0] != "new" || got[1] != "feat" || got[2] != "--detach" {
		t.Errorf("args = %v", got)
	}
	if jobEvents == nil {
		t.Fatal("the event file must open")
	}
	jobEvents.Finish(nil, "done")
	b, _ := os.ReadFile(path)
	if !bytes.Contains(b, []byte(`"kind":"done"`)) {
		t.Errorf("event file = %q", b)
	}
}
