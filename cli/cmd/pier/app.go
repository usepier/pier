package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kerem-kaynak/pier/internal/config"
	"github.com/kerem-kaynak/pier/internal/driver"
	"github.com/kerem-kaynak/pier/internal/proxy"
)

const appAPIVersion = 1

// tmux serializes control characters in format output as their octal escape,
// so the wire delimiter is the literal four-character sequence `\037`.
const appTmuxSeparator = `\037`

type appAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type appAPIEnvelope struct {
	Version int          `json:"version"`
	Data    any          `json:"data,omitempty"`
	Error   *appAPIError `json:"error,omitempty"`
}

type appDependency struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Detail    string `json:"detail"`
}

type appSetupStatus struct {
	Configured   bool            `json:"configured"`
	ConfigPath   string          `json:"configPath"`
	CLIVersion   string          `json:"cliVersion"`
	Profiles     []string        `json:"profiles"`
	Dependencies []appDependency `json:"dependencies"`
}

type appInstance struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Repo         string       `json:"repo"`
	Branch       string       `json:"branch"`
	User         string       `json:"user"`
	Driver       string       `json:"driver"`
	State        driver.State `json:"state"`
	Strained     bool         `json:"strained"`
	Setup        string       `json:"setup"`
	InstanceType string       `json:"instanceType"`
	CreatedAt    string       `json:"createdAt"`
	CostNote     string       `json:"costNote"`
	LocalPath    string       `json:"localPath,omitempty"`
	ProjectID    string       `json:"projectID,omitempty"`
}

type appProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type appBranchOptions struct {
	Project       appProject `json:"project"`
	Branches      []string   `json:"branches"`
	DefaultBranch string     `json:"defaultBranch"`
	FetchWarning  string     `json:"fetchWarning,omitempty"`
}

type appTab struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Active           bool   `json:"active"`
	Panes            int    `json:"panes"`
	Command          string `json:"command"`
	WorkingDirectory string `json:"workingDirectory"`
}

type appPort struct {
	Number  int    `json:"number"`
	Process string `json:"process,omitempty"`
	IsHTTP  bool   `json:"isHTTP"`
	Scheme  string `json:"scheme,omitempty"`
}

const (
	appTabsMarker   = "---PIER-APP-TABS---"
	appPortsMarker  = "---PIER-APP-PORTS---"
	appDockerMarker = "---PIER-APP-DOCKER---"
)

var appSSProcess = regexp.MustCompile(`users:\(\(\"([^\"]+)\"`)
var appDockerPort = regexp.MustCompile(`(?:^|[, ])(?:[^, ]*:)?([0-9]+)(?:-([0-9]+))?->[0-9]+(?:-[0-9]+)?/(?:tcp|udp)`)

type appSnapshot struct {
	Instance  appInstance `json:"instance"`
	ProxyHost string      `json:"proxyHost"`
	Tabs      []appTab    `json:"tabs"`
	Ports     []appPort   `json:"ports"`
}

type appProxyNetworkStatus struct {
	NeedsSetup bool `json:"needsSetup"`
}

func cmdApp(args []string) {
	if len(args) == 0 {
		appWriteError("usage", "usage: pier app <status|login|projects|project-add|branches|instance-new|instance-remove|list|inspect|tab-new|tab-close|port-forward|attach>")
	}

	switch args[0] {
	case "status":
		appWrite(appStatus())
	case "login":
		if err := appAWSLogin(); err != nil {
			appWriteError("aws_login_failed", err.Error())
		}
		appWrite(struct{}{})
	case "projects":
		projects, err := appDiscoverProjects()
		if err != nil {
			appWriteError("projects_failed", err.Error())
		}
		appWrite(projects)
	case "project-add":
		if len(args) != 2 {
			appWriteError("usage", "usage: pier app project-add <repository-path>")
		}
		project, err := appResolveProject(args[1])
		if err != nil {
			appWriteError("project_add_failed", err.Error())
		}
		if err := config.RememberProject(project.ID); err != nil {
			appWriteError("project_add_failed", err.Error())
		}
		appWrite(project)
	case "branches":
		if len(args) != 2 {
			appWriteError("usage", "usage: pier app branches <project-path>")
		}
		options, err := appBranches(args[1])
		if err != nil {
			appWriteError("branches_failed", err.Error())
		}
		appWrite(options)
	case "instance-new":
		instance, err := appCreateInstance(args[1:])
		if err != nil {
			appWriteError("instance_create_failed", err.Error())
		}
		appWrite(instance)
	case "instance-remove":
		if len(args) != 2 {
			appWriteError("usage", "usage: pier app instance-remove <instance>")
		}
		_, drv, session, err := appFindSession(args[1])
		if err != nil {
			appWriteError("instance_not_found", err.Error())
		}
		if err := drv.Destroy(context.Background(), session.ID); err != nil {
			code, message := appCloudError("instance_remove_failed", err)
			appWriteError(code, message)
		}
		_ = config.ForgetWorkspace(session.ID)
		appWrite(struct{}{})
	case "proxy-network-status":
		appWrite(appProxyNetworkStatus{NeedsSetup: proxy.NetworkSetupNeeded()})
	case "proxy-network-setup":
		if err := proxy.EnsureNet(os.Stderr); err != nil {
			appWriteError("proxy_network_setup_failed", err.Error())
		}
		appWrite(struct{}{})
	case "list":
		_, drv, err := appLoadDriver()
		if err != nil {
			appWriteError("not_configured", err.Error())
		}
		sessions, err := drv.List(context.Background())
		if err != nil {
			code, message := appCloudError("list_failed", err)
			appWriteError(code, message)
		}
		enrich(drv, sessions)
		items := make([]appInstance, 0, len(sessions))
		for _, session := range sessions {
			items = append(items, appInstanceFrom(session))
		}
		appWrite(items)
	case "inspect":
		if len(args) != 2 {
			appWriteError("usage", "usage: pier app inspect <instance>")
		}
		_, drv, session, err := appFindSession(args[1])
		if err != nil {
			appWriteError("instance_not_found", err.Error())
		}
		snapshot, err := inspectForApp(drv, session)
		if err != nil {
			appWriteError("inspect_failed", err.Error())
		}
		appWrite(snapshot)
	case "tab-new":
		tab, err := appCreateTab(args[1:])
		if err != nil {
			appWriteError("tab_create_failed", err.Error())
		}
		appWrite(tab)
	case "tab-close":
		if len(args) != 3 {
			appWriteError("usage", "usage: pier app tab-close <instance> <tab-id>")
		}
		_, drv, session, err := appFindSession(args[1])
		if err != nil {
			appWriteError("instance_not_found", err.Error())
		}
		if err := validateTabID(args[2]); err != nil {
			appWriteError("invalid_tab", err.Error())
		}
		if _, err := drv.Exec(context.Background(), session.ID, appCloseTabRemoteCommand(args[2])); err != nil {
			appWriteError("tab_close_failed", err.Error())
		}
		appWrite(struct{}{})
	case "attach":
		if err := appAttachTab(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "pier:", err)
			os.Exit(1)
		}
	case "port-forward":
		if err := appRunPortForward(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "pier:", err)
			os.Exit(1)
		}
	default:
		appWriteError("unknown_command", "unknown app command "+strconv.Quote(args[0]))
	}
}

func appAWSLogin() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := appQuarantineInvalidSSOCaches(); err != nil {
		return err
	}
	args := []string{"sso", "login"}
	if cfg.AWS.Profile != "" {
		args = append(args, "--profile", cfg.AWS.Profile)
	}
	cmd := exec.Command("aws", args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

// An interrupted AWS CLI write can leave an incomplete JSON token cache. The
// CLI then fails before it can refresh the session. Login is an explicit user
// action, so move only malformed cache files aside (rather than deleting
// them) and let AWS recreate them.
func appQuarantineInvalidSSOCaches() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	paths, err := filepath.Glob(filepath.Join(home, ".aws", "sso", "cache", "*.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		contents, readErr := os.ReadFile(path)
		if readErr != nil || json.Valid(contents) {
			continue
		}
		backup := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.Rename(path, backup); err != nil {
			return fmt.Errorf("preserve invalid AWS SSO cache %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

var appAWSCacheHash = regexp.MustCompile(`'([a-f0-9]{40})'`)

func appCloudError(fallbackCode string, err error) (string, string) {
	message := err.Error()
	if match := appAWSCacheHash.FindStringSubmatch(message); len(match) == 2 {
		home, homeErr := os.UserHomeDir()
		if homeErr == nil {
			cache := filepath.Join(home, ".aws", "sso", "cache", match[1]+".json")
			contents, readErr := os.ReadFile(cache)
			if readErr != nil || json.Valid(contents) {
				return fallbackCode, message
			}
			cfg, _ := config.Load()
			profile := cfg.AWS.Profile
			if profile == "" {
				profile = "default"
			}
			return "aws_login_required", "The AWS SSO cache for profile \"" + profile + "\" is incomplete. Sign in again, then Pier can reload your instances."
		}
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "sso") && (strings.Contains(lower, "expired") || strings.Contains(lower, "login")) {
		return "aws_login_required", "Your AWS SSO session has expired. Sign in again, then Pier can reload your instances."
	}
	return fallbackCode, message
}

func appDiscoverProjects() ([]appProject, error) {
	candidates := make([]string, 0)
	candidates = append(candidates, config.ProjectPaths()...)
	for _, path := range config.WorkspacePaths() {
		candidates = append(candidates, path)
	}
	return appCollectProjects(candidates), nil
}

func appCollectProjects(candidates []string) []appProject {
	byPath := map[string]appProject{}
	for _, candidate := range candidates {
		root, err := exec.Command("git", "-C", candidate, "rev-parse", "--show-toplevel").Output()
		if err != nil {
			continue
		}
		path := strings.TrimSpace(string(root))
		if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
			path = resolved
		}
		if path == "" {
			continue
		}
		byPath[path] = appProject{ID: path, Name: filepath.Base(path), Path: appAbbreviateHome(path)}
	}
	projects := make([]appProject, 0, len(byPath))
	for _, project := range byPath {
		projects = append(projects, project)
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].Name == projects[j].Name {
			return projects[i].ID < projects[j].ID
		}
		return strings.ToLower(projects[i].Name) < strings.ToLower(projects[j].Name)
	})
	return projects
}

func appResolveProject(path string) (appProject, error) {
	root, err := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return appProject{}, fmt.Errorf("%s is not a Git repository", path)
	}
	resolved := strings.TrimSpace(string(root))
	if evaluated, resolveErr := filepath.EvalSymlinks(resolved); resolveErr == nil {
		resolved = evaluated
	}
	return appProject{ID: resolved, Name: filepath.Base(resolved), Path: appAbbreviateHome(resolved)}, nil
}

func appBranches(path string) (appBranchOptions, error) {
	project, err := appResolveProject(path)
	if err != nil {
		return appBranchOptions{}, err
	}
	warning := ""
	if err := exec.Command("git", "-C", project.ID, "remote", "get-url", "origin").Run(); err == nil {
		if output, fetchErr := exec.Command("git", "-C", project.ID, "fetch", "--prune", "--quiet", "origin").CombinedOutput(); fetchErr != nil {
			warning = "Could not refresh origin; showing locally cached branches."
			if detail := strings.TrimSpace(string(output)); detail != "" {
				warning += " " + detail
			}
		}
	}
	output, err := exec.Command(
		"git", "-C", project.ID, "for-each-ref", "--format=%(refname:short)", "refs/heads", "refs/remotes/origin",
	).Output()
	if err != nil {
		return appBranchOptions{}, fmt.Errorf("list branches: %w", err)
	}
	branches := appNormalizeBranches(strings.Split(string(output), "\n"))
	if len(branches) == 0 {
		return appBranchOptions{}, fmt.Errorf("%s has no branches", project.Name)
	}
	defaultBranch := ""
	if output, err := exec.Command("git", "-C", project.ID, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output(); err == nil {
		defaultBranch = strings.TrimSpace(string(output))
	}
	if defaultBranch == "" {
		if output, err := exec.Command("git", "-C", project.ID, "branch", "--show-current").Output(); err == nil {
			defaultBranch = strings.TrimSpace(string(output))
		}
	}
	if defaultBranch == "" || !containsString(branches, defaultBranch) {
		defaultBranch = branches[0]
	}
	return appBranchOptions{
		Project: project, Branches: branches, DefaultBranch: defaultBranch, FetchWarning: warning,
	}, nil
}

func appNormalizeBranches(values []string) []string {
	seen := map[string]bool{}
	branches := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "origin/HEAD" || strings.HasPrefix(value, "origin/HEAD -> ") || seen[value] {
			continue
		}
		seen[value] = true
		branches = append(branches, value)
	}
	sort.SliceStable(branches, func(i, j int) bool {
		iRemote := strings.HasPrefix(branches[i], "origin/")
		jRemote := strings.HasPrefix(branches[j], "origin/")
		if iRemote != jRemote {
			return iRemote
		}
		return strings.ToLower(branches[i]) < strings.ToLower(branches[j])
	})
	return branches
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func appCreateInstance(args []string) (appInstance, error) {
	if len(args) != 3 {
		return appInstance{}, fmt.Errorf("usage: pier app instance-new <project-path> <name> <base-branch>")
	}
	project, err := appResolveProject(args[0])
	if err != nil {
		return appInstance{}, err
	}
	name, base := args[1], args[2]
	if !appSessionName.MatchString(name) || strings.Contains(name, "..") || strings.Contains(name, "//") || strings.HasSuffix(name, ".lock") {
		return appInstance{}, fmt.Errorf("name must use 1-100 letters, digits, dots, underscores, dashes, or slashes")
	}
	if base == "" {
		return appInstance{}, fmt.Errorf("base branch cannot be empty")
	}
	if err := config.RememberProject(project.ID); err != nil {
		return appInstance{}, fmt.Errorf("remember project: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return appInstance{}, err
	}
	cmd := exec.Command(executable, name, base, "--detach")
	cmd.Dir = project.ID
	cmd.Env = append(os.Environ(), "PIER_CREATE_EVENTS=json")
	// Keep stdout reserved for the app API's final JSON envelope, but stream
	// the normal create command's progress to stderr as it happens. The native
	// app reads that diagnostics channel line-by-line for its Setup view.
	var output bytes.Buffer
	progress := io.MultiWriter(os.Stderr, &output)
	cmd.Stdout = progress
	cmd.Stderr = progress
	err = cmd.Run()
	if err != nil {
		message := strings.TrimSpace(output.String())
		if message == "" {
			message = err.Error()
		}
		return appInstance{}, fmt.Errorf("%s", message)
	}
	_, _, session, err := appFindSession(name)
	if err != nil {
		return appInstance{}, err
	}
	return appInstanceFrom(session), nil
}

var appSessionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)

// appAttachTab is the one interactive app command. The native macOS terminal
// gives this process a PTY; ssh carries that PTY to the instance and tmux
// reconnects it to the selected window. Ending the local process only detaches
// the client — the remote tmux window and everything inside it keep running.
func appAttachTab(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: pier app attach <instance> <tab-id>")
	}
	if err := validateTabID(args[1]); err != nil {
		return err
	}
	_, drv, err := appLoadDriver()
	if err != nil {
		return err
	}
	// The app passes the provider instance ID from its already-loaded
	// snapshot. Do not call List here: doing so re-runs caller identity inside
	// every terminal PTY before SSH can even start. Tabs only exist on running
	// instances, so attach directly to the known target.
	instanceID := args[0]
	options, target, err := drv.SSHTarget(context.Background(), instanceID)
	if err != nil {
		return err
	}
	remote := appAttachRemoteCommand(args[1])
	sshArgs := append(options, "-t", "-o", "ForwardAgent=yes", target, remote)
	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func appAttachRemoteCommand(tabID string) string {
	return `[ -S "$SSH_AUTH_SOCK" ] && ln -sf "$SSH_AUTH_SOCK" ~/.ssh/agent.sock
[ -e "$HOME/.pier-bootstrapped" ] || { echo "pier: this session is still setting up" >&2; exit 1; }
client_session="pier-app-$$"
cleanup() { tmux kill-session -t "$client_session" 2>/dev/null || true; }
trap cleanup EXIT HUP INT TERM
tmux set-option -g set-titles on
tmux set-option -g set-titles-string '#{pane_current_command}'
window_index=$(tmux display-message -p -t ` + shellQuote(tabID) + ` '#{window_index}') || exit 1
tmux new-session -d -t main -s "$client_session"
tmux select-window -t "$client_session:$window_index"
tmux attach-session -t "$client_session"`
}

func appStatus() appSetupStatus {
	_, err := config.Load()
	profiles := []string{}
	if out, profileErr := exec.Command("aws", "configure", "list-profiles").Output(); profileErr == nil {
		profiles = strings.Fields(string(out))
	}

	tools := []struct {
		name string
		bin  string
		hint string
	}{
		{"AWS CLI", "aws", "Install the AWS CLI to manage account resources."},
		{"Session Manager plugin", "session-manager-plugin", "Install the AWS Session Manager plugin for tunnel fallback."},
		{"OpenSSH", "ssh", "OpenSSH provides terminal and port-forward transports."},
		{"Git", "git", "Git provides local repository snapshots."},
	}
	dependencies := make([]appDependency, 0, len(tools))
	for _, tool := range tools {
		found, lookupErr := exec.LookPath(tool.bin)
		if lookupErr != nil {
			dependencies = append(dependencies, appDependency{Name: tool.name, Detail: tool.hint})
			continue
		}
		dependencies = append(dependencies, appDependency{Name: tool.name, Available: true, Detail: found})
	}

	return appSetupStatus{
		Configured:   err == nil,
		ConfigPath:   config.Path(),
		CLIVersion:   version,
		Profiles:     profiles,
		Dependencies: dependencies,
	}
}

func appLoadDriver() (config.Config, driver.Driver, error) {
	cfg, err := config.Load()
	if err != nil {
		return cfg, nil, err
	}
	drv, err := newDriver(cfg)
	return cfg, drv, err
}

func appFindSession(query string) (config.Config, driver.Driver, driver.Session, error) {
	cfg, drv, err := appLoadDriver()
	if err != nil {
		return cfg, nil, driver.Session{}, err
	}
	sessions, err := drv.List(context.Background())
	if err != nil {
		return cfg, drv, driver.Session{}, err
	}
	var matches []driver.Session
	for _, session := range sessions {
		if session.ID == query || session.Name == query {
			return cfg, drv, session, nil
		}
		if strings.Contains(session.Name, query) {
			matches = append(matches, session)
		}
	}
	if len(matches) == 1 {
		return cfg, drv, matches[0], nil
	}
	if len(matches) > 1 {
		return cfg, drv, driver.Session{}, fmt.Errorf("%q is ambiguous", query)
	}
	return cfg, drv, driver.Session{}, fmt.Errorf("no instance matching %q", query)
}

func appInstanceFrom(session driver.Session) appInstance {
	created := ""
	if !session.Created.IsZero() {
		created = session.Created.UTC().Format(time.RFC3339)
	}
	projectID, localPath := appLocalWorkspace(session.ID, session.Repo)
	return appInstance{
		ID:           session.ID,
		Name:         session.Name,
		Repo:         session.Repo,
		Branch:       session.Branch,
		User:         session.User,
		Driver:       session.Driver,
		State:        session.State,
		Strained:     session.Strained,
		Setup:        session.Setup,
		InstanceType: session.InstanceType,
		CreatedAt:    created,
		CostNote:     session.CostNote,
		LocalPath:    localPath,
		ProjectID:    projectID,
	}
}

func appLocalWorkspace(instanceID, repo string) (string, string) {
	if path := config.WorkspacePaths()[instanceID]; path != "" {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		return path, appAbbreviateHome(path)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", repo
	}
	for _, parent := range []string{"Documents", "Developer", "Projects", "Code", "src"} {
		candidate := filepath.Join(home, parent, repo)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, appAbbreviateHome(candidate)
		}
	}
	return "", repo
}

func appAbbreviateHome(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && path != home && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func inspectForApp(drv driver.Driver, session driver.Session) (appSnapshot, error) {
	if session.State == driver.StateCreating {
		return appSnapshot{}, fmt.Errorf("%s is still being created", session.Name)
	}
	if session.State == driver.StateParked {
		return appSnapshot{Instance: appInstanceFrom(session), ProxyHost: proxy.Hostname(session.Name) + ".pier", Tabs: []appTab{}, Ports: []appPort{}}, nil
	}

	remote := "cat /run/pier/status.json 2>/dev/null || printf '{}'; printf '\\n" + appTabsMarker + "\\n'; " +
		"tmux list-windows -t main -F " + shellQuote(appTmuxWindowFormat()) + " 2>/dev/null || true; " +
		"printf '%s\\n' '" + appPortsMarker + "'; sudo -n ss -Hlnpt 2>/dev/null || true; " +
		"printf '%s\\n' '" + appDockerMarker + "'; sudo -n docker ps --format '{{.Names}}\\t{{.Ports}}' 2>/dev/null || true"
	out, err := drv.Exec(context.Background(), session.ID, remote)
	if err != nil {
		return appSnapshot{}, err
	}
	statusRaw, remainder, ok := strings.Cut(out, appTabsMarker)
	if !ok {
		return appSnapshot{}, fmt.Errorf("session returned an incomplete inspection response")
	}
	tabsRaw, portRemainder, ok := strings.Cut(remainder, appPortsMarker)
	if !ok {
		return appSnapshot{}, fmt.Errorf("session returned incomplete port details")
	}
	portsRaw, dockerRaw, ok := strings.Cut(portRemainder, appDockerMarker)
	if !ok {
		return appSnapshot{}, fmt.Errorf("session returned incomplete container port details")
	}
	var status struct {
		State     string `json:"state"`
		Listening []int  `json:"listening"`
		Strained  bool   `json:"strained"`
		Setup     string `json:"setup"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(statusRaw)), &status)
	if status.State == "working" {
		session.State = driver.StateWorking
	} else if status.State == "idle" {
		session.State = driver.StateIdle
	}
	session.Strained = status.Strained
	session.Setup = status.Setup

	tabs := []appTab{}
	for _, line := range strings.Split(strings.TrimSpace(tabsRaw), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, appTmuxSeparator)
		if len(fields) != 6 {
			continue
		}
		panes, _ := strconv.Atoi(fields[3])
		tabs = append(tabs, appTab{
			ID: fields[0], Name: fields[1], Active: fields[2] == "1", Panes: panes,
			Command: fields[4], WorkingDirectory: fields[5],
		})
	}
	processes := appListeningProcesses(portsRaw)
	containerPorts := appDockerPortProcesses(dockerRaw)
	ports := make([]appPort, 0, len(status.Listening))
	for _, number := range status.Listening {
		scheme := webScheme(number)
		process := processes[number]
		if process == "docker-proxy" && containerPorts[number] != "" {
			process = containerPorts[number] + " (Docker)"
		}
		ports = append(ports, appPort{
			Number: number, Process: process, IsHTTP: scheme != "", Scheme: scheme,
		})
	}
	return appSnapshot{Instance: appInstanceFrom(session), ProxyHost: proxy.Hostname(session.Name) + ".pier", Tabs: tabs, Ports: ports}, nil
}

func appDockerPortProcesses(output string) map[int]string {
	processes := map[int]string{}
	for _, line := range strings.Split(output, "\n") {
		name, ports, ok := strings.Cut(line, "\t")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		for _, match := range appDockerPort.FindAllStringSubmatch(ports, -1) {
			start, _ := strconv.Atoi(match[1])
			end := start
			if match[2] != "" {
				end, _ = strconv.Atoi(match[2])
			}
			if start > 0 && end >= start && end-start < 1000 {
				for port := start; port <= end; port++ {
					processes[port] = strings.TrimSpace(name)
				}
			}
		}
	}
	return processes
}

func appListeningProcesses(output string) map[int]string {
	processes := map[int]string{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "LISTEN" {
			continue
		}
		port := appAddressPort(fields[3])
		if port == 0 {
			continue
		}
		if match := appSSProcess.FindStringSubmatch(line); len(match) == 2 {
			processes[port] = match[1]
		}
	}
	return processes
}

func appAddressPort(address string) int {
	index := strings.LastIndexByte(address, ':')
	if index < 0 {
		return 0
	}
	port, _ := strconv.Atoi(address[index+1:])
	return port
}

func appTmuxWindowFormat() string {
	return strings.Join([]string{
		"#{window_id}", "#{window_name}", "#{window_active}",
		"#{window_panes}", "#{pane_current_command}", "#{pane_current_path}",
	}, appTmuxSeparator)
}

var appTabName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var appTabID = regexp.MustCompile(`^@[0-9]+$`)

func appCreateTab(args []string) (appTab, error) {
	if len(args) < 1 {
		return appTab{}, fmt.Errorf("usage: pier app tab-new <instance> --name <name> -- <command> [args...]")
	}
	instanceQuery := args[0]
	name := "shell"
	command := []string{"bash"}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i+1 >= len(args) {
				return appTab{}, fmt.Errorf("--name needs a value")
			}
			i++
			name = args[i]
		case "--":
			if i+1 < len(args) {
				command = args[i+1:]
			}
			i = len(args)
		default:
			return appTab{}, fmt.Errorf("unexpected argument %q", args[i])
		}
	}
	if !appTabName.MatchString(name) {
		return appTab{}, fmt.Errorf("tab name must use letters, numbers, dots, dashes, or underscores")
	}
	if len(command) == 0 {
		return appTab{}, fmt.Errorf("tab command cannot be empty")
	}

	_, drv, session, err := appFindSession(instanceQuery)
	if err != nil {
		return appTab{}, err
	}
	if session.State == driver.StateCreating {
		return appTab{}, fmt.Errorf("%s is still being created", session.Name)
	}
	if session.State == driver.StateParked {
		if err := drv.Resume(context.Background(), session.ID); err != nil {
			return appTab{}, err
		}
		if err := waitReachable(drv, session.ID, 4*time.Minute); err != nil {
			return appTab{}, err
		}
	}

	windowParts := []string{
		"tmux", "new-window", "-d", "-P", "-F", "#{window_id}",
		"-t", "main", "-n", name, "-c", "/home/agent/work/" + session.Repo, "--",
	}
	windowParts = append(windowParts, command...)
	newSessionParts := []string{
		"tmux", "new-session", "-d", "-P", "-F", "#{window_id}",
		"-s", "main", "-n", name, "-c", "/home/agent/work/" + session.Repo, "--",
	}
	newSessionParts = append(newSessionParts, command...)
	remote := appCreateTabRemoteCommand(windowParts, newSessionParts)
	out, err := drv.Exec(context.Background(), session.ID, remote)
	if err != nil {
		return appTab{}, err
	}
	return appTab{
		ID: strings.TrimSpace(out), Name: name, Panes: 1,
		Command: command[0], WorkingDirectory: "/home/agent/work/" + session.Repo,
	}, nil
}

func appCreateTabRemoteCommand(windowParts, newSessionParts []string) string {
	quoteCommand := func(parts []string) string {
		quoted := make([]string, len(parts))
		for i, part := range parts {
			quoted[i] = shellQuote(part)
		}
		return strings.Join(quoted, " ")
	}
	return "if tmux has-session -t main 2>/dev/null; then " + quoteCommand(windowParts) +
		"; else " + quoteCommand(newSessionParts) + "; fi"
}

// Closing a tab is intentionally idempotent. A terminal command can exit on
// its own between the last inspection and a click on the close button, and
// duplicate UI events must not turn that normal race into an alert.
func appCloseTabRemoteCommand(tabID string) string {
	return "tmux kill-window -t " + shellQuote(tabID) + " 2>/dev/null || true"
}

func validateTabID(id string) error {
	if !appTabID.MatchString(id) {
		return fmt.Errorf("invalid tmux window id %q", id)
	}
	return nil
}

func likelyHTTPPort(port int) bool {
	return webScheme(port) != ""
}

func webScheme(port int) string {
	switch port {
	case 443, 8443:
		return "https"
	case 80, 3000, 3001, 4173, 4200, 5000, 5173, 6333, 8000, 8080, 8081, 8088, 8089, 8888, 9000, 9001:
		return "http"
	default:
		return ""
	}
}

func appRunPortForward(args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: pier app port-forward <instance> <local-port> <remote-port>")
	}
	local, err := appValidPort(args[1])
	if err != nil {
		return fmt.Errorf("local port: %w", err)
	}
	remote, err := appValidPort(args[2])
	if err != nil {
		return fmt.Errorf("remote port: %w", err)
	}
	_, drv, session, err := appFindSession(args[0])
	if err != nil {
		return err
	}
	if session.State == driver.StateParked || session.State == driver.StateCreating {
		return fmt.Errorf("%s is %s", session.Name, session.State)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd, err := drv.PortForwardCommand(ctx, session.ID, [][2]int{{local, remote}})
	if err != nil {
		return err
	}
	return cmd.Run()
}

func appValidPort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%q is not a valid TCP port", value)
	}
	return port, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func appWrite(data any) {
	_ = json.NewEncoder(os.Stdout).Encode(appAPIEnvelope{Version: appAPIVersion, Data: data})
}

func appWriteError(code, message string) {
	_ = json.NewEncoder(os.Stdout).Encode(appAPIEnvelope{
		Version: appAPIVersion,
		Error:   &appAPIError{Code: code, Message: message},
	})
	os.Exit(1)
}
