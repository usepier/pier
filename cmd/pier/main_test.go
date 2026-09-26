package main

import (
	"strings"
	"testing"
	"time"

	"github.com/usepier/pier/internal/driver"
)

// "create failed" and "setup failed" sit in the same column and mean opposite
// things: one has no VM at all, the other has a running one whose setup
// script died. They must never render as the same word.
func TestStateLabelDistinguishesCreateFromSetupFailure(t *testing.T) {
	failed := stateLabel(driver.Session{State: driver.StateFailed})
	if failed != "create failed" {
		t.Errorf(`want "create failed", got %q`, failed)
	}
	setup := stateLabel(driver.Session{State: driver.StateIdle, Setup: "failed"})
	if !strings.Contains(setup, "idle") || !strings.Contains(setup, "setup failed") {
		t.Errorf("a live session with a dead setup script must still read as running, got %q", setup)
	}
}

func TestWriteSessionsJSON(t *testing.T) {
	created := time.Date(2026, time.August, 14, 9, 30, 0, 0, time.FixedZone("test", 4*60*60))
	sessions := []driver.Session{{
		ID: "i-123", Name: "json-output", Repo: "pier", Branch: "feat/json",
		User: "arn:aws:iam::123:user/test", Driver: "aws-ec2",
		State: driver.StateWorking, Strained: true, Setup: "running",
		InstanceType: "t4g.medium", Created: created, CostNote: "~$0.04/h",
	}}

	var out strings.Builder
	if err := writeSessionsJSON(&out, sessions); err != nil {
		t.Fatal(err)
	}
	want := `[{"id":"i-123","name":"json-output","repository":"pier","branch":"feat/json","owner":"arn:aws:iam::123:user/test","provider":"aws-ec2","state":"working","setup_state":"running","strained":true,"created_at":"2026-08-14T05:30:00Z","machine_type":"t4g.medium","cost_note":"~$0.04/h"}]` + "\n"
	if got := out.String(); got != want {
		t.Fatalf("unexpected JSON output\nwant: %s\n got: %s", want, got)
	}
}

func TestWriteSessionsJSONEmpty(t *testing.T) {
	var out strings.Builder
	if err := writeSessionsJSON(&out, nil); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "[]\n" {
		t.Fatalf("empty session list must be a JSON array, got %q", got)
	}
}

func TestParseLSArgs(t *testing.T) {
	jsonOutput, err := parseLSArgs([]string{"--json"})
	if err != nil || !jsonOutput {
		t.Fatalf("--json should enable JSON output, got json=%v err=%v", jsonOutput, err)
	}
	if _, err := parseLSArgs([]string{"extra"}); err == nil {
		t.Error("positional arguments must be rejected")
	}
	if _, err := parseLSArgs([]string{"--unknown"}); err == nil {
		t.Error("unknown flags must be rejected")
	}
}
