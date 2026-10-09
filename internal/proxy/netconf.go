package proxy

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/usepier/pier/internal/ui"
)

// bootLabel names the boot task that puts the loopback addresses back
// after a restart.
const bootLabel = "com.usepier.pier.loopback"

var bootPlist = "/Library/LaunchDaemons/" + bootLabel + ".plist"

// bootTask is the boot task's launchd job: one shell line adding every
// address the proxy binds, run as the system starts.
func bootTask() string {
	var adds []string
	for _, ip := range proxyIPs() {
		adds = append(adds, "/sbin/ifconfig lo0 alias "+ip+" 255.255.255.255")
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>` + bootLabel + `</string>
	<key>ProgramArguments</key>
	<array><string>/bin/sh</string><string>-c</string><string>` + strings.Join(adds, "; ") + `</string></array>
	<key>RunAtLoad</key><true/>
</dict>
</plist>
`
}

const resolverFile = "/etc/resolver/" + domain

func resolverContent() string {
	return "nameserver " + dnsAddr + "\nport " + dnsPort + "\ntimeout 1\n"
}

// netSteps are the root commands the network setup still needs (none
// when it is in place), and the loopback addresses missing.
func netSteps() (steps []string, missing []string) {
	content := resolverContent()
	if b, err := os.ReadFile(resolverFile); err != nil || string(b) != content {
		esc := strings.ReplaceAll(content, "\n", `\n`)
		steps = append(steps, "mkdir -p /etc/resolver && printf '"+esc+"' > "+resolverFile)
	}
	// The boot task keeps the addresses across restarts, so the password
	// is asked once, not after every boot.
	if b, err := os.ReadFile(bootPlist); err != nil || string(b) != bootTask() {
		steps = append(steps,
			// One line: the administrator prompt runs the script as one.
			"printf '"+strings.ReplaceAll(bootTask(), "\n", `\n`)+"' > "+bootPlist,
			"chown root:wheel "+bootPlist+" && chmod 644 "+bootPlist,
			"launchctl bootout system/"+bootLabel+" 2>/dev/null || true",
			"launchctl bootstrap system "+bootPlist)
	}
	have := lo0Aliases()
	for _, ip := range proxyIPs() {
		if !have[ip] {
			missing = append(missing, ip)
			steps = append(steps, "ifconfig lo0 alias "+ip+" 255.255.255.255")
		}
	}
	return steps, missing
}

// NetReady reports whether the network setup is in place.
func NetReady() bool {
	steps, _ := netSteps()
	return len(steps) == 0
}

// ForgetNet takes the network setup away again: the resolver file, the
// boot task and the loopback addresses. privileged runs it as root (nil:
// sudo, in the terminal).
func ForgetNet(privileged func(script string) error) error {
	steps := []string{
		"rm -f " + resolverFile,
		"launchctl bootout system/" + bootLabel + " 2>/dev/null || true",
		"rm -f " + bootPlist,
	}
	have := lo0Aliases()
	for _, ip := range proxyIPs() {
		if have[ip] {
			steps = append(steps, "ifconfig lo0 -alias "+ip)
		}
	}
	return runPrivileged(strings.Join(steps, "\n"), privileged)
}

func runPrivileged(script string, privileged func(script string) error) error {
	if privileged != nil {
		return privileged(script)
	}
	cmd := exec.Command("sudo", "/bin/sh", "-ec", script)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// EnsureNet puts the macOS prerequisites in place, with one administrator
// prompt and full disclosure of what runs:
//
//   - /etc/resolver/pier — tells macOS to send *.pier lookups to our
//     resolver (the same split-DNS mechanism VPNs and Tailscale use). The
//     port directive points it at our unprivileged high port. Written once,
//     survives everything; rewritten only if the content is stale.
//   - loopback aliases for 127.94.0.x — macOS only answers 127.0.0.1 out of
//     the box; each session's IP (and the resolver's) must be added to lo0.
//     Inert /32s, gone at reboot — and put back as the Mac starts by a
//     boot task (a launchd job), so the prompt comes only once.
//
// Everything else the proxy does is unprivileged and dies with the process.
// privileged runs the setup as root (nil: sudo, in the terminal).
func EnsureNet(out io.Writer, privileged func(script string) error) error {
	steps, missing := netSteps()
	if len(steps) == 0 {
		return nil
	}
	if out == nil {
		out = io.Discard
	}
	fmt.Fprintln(out, ui.Bold.Render("network setup")+ui.Dim.Render(" — as root:"))
	fmt.Fprintln(out, ui.Dim.Render("  /etc/resolver/"+domain+" and a boot task ("+bootPlist+") that adds the addresses below as the Mac starts"))
	if len(missing) > 0 {
		fmt.Fprintf(out, ui.Dim.Render("  ifconfig lo0 alias %s 255.255.255.255   (%d loopback IPs)\n"), missing[0], len(missing))
	}
	if err := runPrivileged(strings.Join(steps, "\n"), privileged); err != nil {
		return fmt.Errorf("network setup: %w", err)
	}
	return nil
}

// lo0Aliases reads which of our IPs are already on the loopback interface.
func lo0Aliases() map[string]bool {
	have := map[string]bool{}
	out, err := exec.Command("ifconfig", "lo0").Output()
	if err != nil {
		return have
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "inet" {
			have[f[1]] = true
		}
	}
	return have
}

// proxyIPs is every loopback IP the proxy may bind: the resolver's plus each
// session slot's public IP (pier's sniffing relay) and its shadow (where the
// ssh forwards bind).
func proxyIPs() []string {
	ips := []string{dnsAddr}
	for i := range slotCount {
		ips = append(ips, slotIP(i).String(), shadowOf(slotIP(i)).String())
	}
	return ips
}
