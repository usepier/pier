package driver

import (
	"errors"
	"testing"
)

func TestTransportBroken(t *testing.T) {
	for in, want := range map[string]bool{
		"ssh: connect to host 3.78.221.163 port 22: Network is unreachable": true,
		"scp: Connection closed":                                     true,
		"client_loop: send disconnect: Broken pipe":                  true,
		"ssh: connect to host 10.0.0.1 port 22: Operation timed out": true,
		// waitSSH has already seen sshd answer, so a refusal during the push
		// is a bounced service, not a shut port — probeDirect reads it the
		// same way, and destroying an instance over it would be absurd.
		"ssh: connect to host 10.0.0.1 port 22: Connection refused": true,
		"no route to host":               true,
		"bootstrap: exit status 1":       false,
		"scp: /tmp/x: Permission denied": false,
		"Error 127: pnpm: No such file":  false,
		"":                               false,
	} {
		if got := TransportBroken(errors.New(in)); got != want {
			t.Errorf("TransportBroken(%q) = %v, want %v", in, got, want)
		}
	}
	if TransportBroken(nil) {
		t.Error("TransportBroken(nil) must be false")
	}
}
