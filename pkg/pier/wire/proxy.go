package wire

// ProxyState is the local proxy: every running session as <name>.pier,
// its listening ports mirrored on this Mac.
type ProxyState struct {
	Running  bool           `json:"running"`
	Sessions []ProxySession `json:"sessions"`
	// CATrusted says https://<session>.pier opens without a warning.
	CATrusted bool   `json:"caTrusted"`
	Error     string `json:"error,omitempty"` // why it stopped, if it failed
}

// ProxySession is one session as the proxy serves it.
type ProxySession struct {
	ID        string `json:"id"`
	Host      string `json:"host"` // name.pier
	Ports     []int  `json:"ports"`
	Localhost []int  `json:"localhost"` // the ports mirrored on localhost too
}

// PortParams forwards (or stops forwarding) a port of a session to this
// Mac.
type PortParams struct {
	SessionID string `json:"sessionId"`
	Local     int    `json:"local"`
	Remote    int    `json:"remote"`
}

// PortForward is a port forwarded by hand.
type PortForward struct {
	SessionID string `json:"sessionId"`
	Local     int    `json:"local"`
	Remote    int    `json:"remote"`
}

// CLIStatus is the pier command on this Mac.
type CLIStatus struct {
	Link      string `json:"link"`      // where the command goes
	Installed bool   `json:"installed"` // it is this app's pier
	// Provided is a pier on PATH that is this app's by another way (the
	// link a Homebrew cask makes): nothing to install.
	Provided string `json:"provided,omitempty"`
	// Stale: Link points at an app's pier that moved or is gone; install
	// again to repair it.
	Stale bool `json:"stale,omitempty"`
	// Other is another pier the shell finds first (a Homebrew formula's).
	Other string `json:"other,omitempty"`
}

// CLIInstallParams installs the pier command; Quiet never asks for an
// administrator (the answer then says it isn't installed).
type CLIInstallParams struct {
	Quiet bool `json:"quiet,omitempty"`
}
