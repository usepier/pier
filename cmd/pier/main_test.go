package main

import (
	"os/exec"
	"testing"
	"time"
)

func TestRetryAttachOnlyForQuickSSHTransportFailure(t *testing.T) {
	exit := func(code string) error {
		return exec.Command("sh", "-c", "exit "+code).Run()
	}
	if !retryAttach(exit("255"), time.Second) {
		t.Error("quick ssh transport failure should retry")
	}
	if retryAttach(exit("1"), time.Second) {
		t.Error("remote command failure should not retry")
	}
	if retryAttach(exit("255"), 16*time.Second) {
		t.Error("slow transport failure should not retry")
	}
}
