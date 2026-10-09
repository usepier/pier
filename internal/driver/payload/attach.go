package payload

import (
	"fmt"
	"regexp"
)

// AttachScript is the remote script every terminal into a session runs
// before handing over to tmux (then: the final command, which execs tmux).
//
// It refreshes ~/.ssh/agent.sock: each connection gets a fresh forwarded
// socket path, while long-lived tmux panes hold the old one — bashrc points
// them at the symlink instead, so `git push` over ssh keeps working across
// reconnects. It refuses on a missing bootstrap marker: attaching mid-create
// would land in an empty $HOME and steal the main tmux session away from its
// workdir. And it waits, briefly and bounded, for the boot-time restore of a
// woken session's tmux layout, so the terminal lands in the restored windows
// instead of racing it to an empty main.
func AttachScript(then string) string {
	return `[ -S "$SSH_AUTH_SOCK" ] && ln -sf "$SSH_AUTH_SOCK" ~/.ssh/agent.sock
[ -e "$HOME/.pier-bootstrapped" ] || { echo "pier: this session is still setting up — attach again when it shows running in pier ls" >&2; exit 1; }
# SSH forwards the client's TERM, but newer terminal emulators
# may not exist in the VM image's terminfo database yet. Keep the richer entry
# when it is installed and otherwise use the portable 256-colour baseline.
if ! infocmp "$TERM" >/dev/null 2>&1; then export TERM=xterm-256color; fi
# A woken session rebuilds its saved tmux layout at boot (pier-restore.service);
# give that a moment so this attach lands in the restored windows instead of
# racing it to an empty main. Sessions without the unit or a saved layout
# never wait.
if [ -f /etc/systemd/system/pier-restore.service ] && [ -f ~/.pier/tmux/state.json ]; then
  for _ in $(seq 30); do [ -e /run/pier/restored ] && break; sleep 0.5; done
fi
` + then
}

// MainAttach is the classic attach: straight into tmux's main session.
const MainAttach = "exec tmux new-session -A -s main"

var tmuxID = regexp.MustCompile(`^@[0-9]+$`)
var tabID = regexp.MustCompile(`^[a-z0-9]{1,24}$`)

// TabAttach attaches a frontend's terminal tab: its own grouped session
// (pier-tab-<tab>) onto main's windows, so each tab shows its own window
// while sharing the rest. The group session goes away when its last client
// detaches. window is a tmux window id (@N), or "" for main's current one.
func TabAttach(tab, window string) (string, error) {
	if !tabID.MatchString(tab) {
		return "", fmt.Errorf("bad tab id %q", tab)
	}
	if window != "" && !tmuxID.MatchString(window) {
		return "", fmt.Errorf("bad window id %q", window)
	}
	s := "pier-tab-" + tab
	script := "tmux has-session -t main 2>/dev/null || tmux new-session -d -s main\n" +
		"tmux kill-session -t " + s + " 2>/dev/null\n" +
		"tmux new-session -d -t main -s " + s + "\n"
	if window != "" {
		script += "tmux select-window -t " + s + ":" + window + " 2>/dev/null\n"
	}
	// destroy-unattached only once attached: tmux (3.4) destroys a session
	// that is unattached when the option is set.
	return script + "exec tmux attach-session -t " + s + " \\; set-option -t " + s + " destroy-unattached on", nil
}
