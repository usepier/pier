package driver

import "strings"

// TransportBroken reports whether err is the connection itself failing rather
// than the remote command exiting nonzero. Only the former says anything
// about which path to use next; a remote command that fails would fail
// identically over the tunnel.
func TransportBroken(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, m := range []string{
		"network is unreachable", // the local route vanished (VPN flap)
		"no route to host",
		"connection reset",
		"connection closed",
		// Refused mid-create means sshd bounced, not that the port is shut:
		// waitSSH has already seen it answer. probeDirect treats a refusal
		// the same way (retry almost immediately), so failing the push on one
		// would destroy an instance over a service restart.
		"connection refused",
		"connection timed out",
		"operation timed out",
		"broken pipe",
		"host is down",
		"host is unreachable",
	} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}
