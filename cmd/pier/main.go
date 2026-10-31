// pier — coding agent sessions as park-when-idle micro-VMs on your own cloud.
//
// This file is the CLI frontend: it parses flags, calls pkg/pier (the API
// every frontend shares), and renders the result. Behavior lives in the API;
// what's here is presentation.
package main

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/usepier/pier/internal/tui"
	"github.com/usepier/pier/internal/ui"
	"github.com/usepier/pier/pkg/pier"
)

//go:embed assets
var assets embed.FS

// version is stamped by make via -ldflags; "dev" outside a release build.
var version = "dev"

func supervisorBin(arch string) ([]byte, error) {
	b, err := assets.ReadFile("assets/pier-supervisor-linux-" + arch)
	if err != nil {
		return nil, fmt.Errorf("supervisor for %s not embedded — build with `make`, not `go build`", arch)
	}
	return b, nil
}

// options are the API options every CLI command runs with: the terminal
// gets transfer meters and transport notices.
func options() pier.Options {
	return pier.Options{
		SupervisorBin: supervisorBin,
		Version:       version,
		Out:           os.Stdout,
		Notify:        func(msg string) { fmt.Fprintln(os.Stderr, ui.Dim.Render("pier: "+msg)) },
	}
}

type helpItem struct {
	command     string
	description string
}

type helpSection struct {
	title string
	items []helpItem
}

var helpSections = []helpSection{
	{
		title: "Sessions",
		items: []helpItem{
			{"pier <branch> [base]", "new session off base (default origin's main branch; HEAD: this clone as it is), then attach"},
			{"pier new <branch> [base]", "the same, for a branch named like a command (new, ls, ...)"},
			{"pier new -p <prompt>", "a session with a stand-in name; its branch is named after the prompt"},
			{"  -d, --detach", "create without attaching"},
			{"  --idle <dur|never>", "park after this much idle time (default from settings)"},
			{"  --cap <dur|never>", "runaway cap for this session"},
			{"  --no-park", "never park this session when idle"},
			{"  --no-pool", "launch fresh instead of claiming a pooled session"},
			{"  --agent <claude|codex>", "start a chat with this agent (default: your default agent)"},
			{"  -p, --prompt <text>", "the chat's first prompt, sent once setup is done"},
			{"  --model <model>", "the chat's model"},
			{"  --effort <level>", "how hard it thinks"},
			{"  --permission-mode <mode>", "what it does without asking"},
			{"  --fast", "the chat's fast mode: faster output from the same model"},
			{"pier ls [--json]", "list sessions"},
			{"pier attach [-a] <session>", "attach; a parked session resumes with its tmux restored (-a: its chats window)"},
			{"  -a, --agent", "open on the session's chats, live (t continues one in the terminal)"},
			{"pier logs <session> [-f]", "show or follow the setup log"},
			{"pier keep <session>", "never park this session when idle"},
			{"pier park <session>", "park a session now (attach wakes it)"},
			{"pier resize <session> <type>", "change the VM size (same CPU architecture)"},
			{"pier rm <session> [-f]", "destroy a session and its disk"},
		},
	},
	{
		title: "Repos",
		items: []helpItem{
			{"pier repos [--json]", "every repo: session image, pool, monthly cost"},
			{"pier bake", "build this repo's session image (repo prebuilt; see settings)"},
			{"  --toolchain-only", "keep repo state out of the image for this bake"},
			{"  --with-repo", "prebuild the repo into the image for this bake"},
			{"pier pool", "this repo's pool: parked, set-up sessions new ones claim"},
			{"pier pool set <n>", "keep n in this repo's pool (0 = off)"},
			{"pier pool fill [-d]", "top the pool up now (e.g. right after a bake)"},
			{"pier pool drain [repo]", "destroy a repo's pooled sessions, from anywhere"},
		},
	},
	{
		title: "Access",
		items: []helpItem{
			{"pier proxy", "sessions as <session>.pier, ports mirrored to localhost (macOS); start | stop | status in the background, forget to undo its setup"},
			{"pier port <session> <port...>", "forward ports by hand (8080:3000 = local:session)"},
		},
	},
	{
		title: "Setup",
		items: []helpItem{
			{"pier setup", "first-time setup (re-run any time)"},
			{"  --print-admin", "print the account setup for a cloud admin to run"},
			{"  --skills", "only install or refresh the bundled agent skills"},
			{"pier doctor [--json]", "check the environment and cloud account"},
			{"pier teardown", "remove all pier groundwork and images from the account"},
			{"pier version", "print the pier version"},
		},
	},
}

func main() {
	args := takeEventsFlag(os.Args[1:])
	defer jobEvents.Finish(nil, "")
	if len(args) == 0 {
		cmdTUI()
		return
	}
	switch args[0] {
	case "api":
		cmdAPI(args[1:])
	case "new":
		cmdNew(args[1:])
	case "ls":
		cmdLS(args[1:])
	case "attach":
		cmdAttach(args[1:])
	case "logs":
		cmdLogs(args[1:])
	case "proxy":
		cmdProxy(args[1:])
	case "port":
		cmdPort(args[1:])
	case "rm":
		cmdRM(args[1:])
	case "keep":
		cmdKeep(args[1:])
	case "park":
		cmdPark(args[1:])
	case "deliver":
		cmdDeliver(args[1:]) // hidden: the background half of sending to a parked session
	case "resize":
		cmdResize(args[1:])
	case "repos":
		cmdRepos(args[1:])
	case "bake":
		cmdBake(args[1:])
	case "pool":
		cmdPool(args[1:])
	case "setup":
		cmdSetup(args[1:])
	case "doctor":
		cmdDoctor(len(os.Args) > 2 && os.Args[2] == "--json")
	case "teardown":
		cmdTeardown()
	case "version", "-v", "--version":
		fmt.Println("pier " + version)
	case "help", "-h", "--help":
		printUsage()
	default:
		cmdNew(args)
	}
}

func printUsage() {
	fmt.Println("\n " + ui.Title.Render("⚓ pier") +
		ui.Dim.Render(" — coding agent sessions as park-when-idle VMs on your own cloud") + "\n")
	command := fmt.Sprintf("%-35s", "pier")
	fmt.Printf("   %s  %s\n", ui.Accent.Render(command), "open the app: sessions, repos, settings")
	for _, section := range helpSections {
		fmt.Println()
		fmt.Println(" " + ui.Bold.Render(section.title))
		for _, item := range section.items {
			command = fmt.Sprintf("%-35s", item.command)
			fmt.Printf("   %s  %s\n", ui.Accent.Render(command), item.description)
		}
	}
	fmt.Println()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, ui.Bad.Render("pier:"), err)
	jobEvents.Finish(err, "")
	os.Exit(1)
}

// jobEvents is set when this process runs as a background job (the hidden
// --events flag a frontend's Start* adds): progress also lands there as
// JSON lines, ending in a done or failed event.
var jobEvents *pier.EventLog

// takeEventsFlag removes `--events <path>` from args, opening the event
// file it names.
func takeEventsFlag(args []string) []string {
	out := args[:0:0]
	for i := 0; i < len(args); i++ {
		if args[i] == "--events" && i+1 < len(args) {
			if l, err := pier.OpenEventLog(args[i+1]); err == nil {
				jobEvents = l
			}
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func open() *pier.Client {
	c, err := pier.Open(options())
	if err != nil {
		fatal(err)
	}
	ui.SetAccent(c.Config().Theme)
	return c
}

func repoRoot() string {
	root, err := pier.RepoRoot("")
	if err != nil {
		fatal(err)
	}
	return root
}

// progress renders API events as the CLI's step lines, each with the time
// since the operation began.
func progress(e pier.Event) {
	jobEvents.Write(e)
	elapsed := ui.Dim.Render(fmt.Sprintf("  +%s", e.Elapsed.Round(time.Second)))
	switch e.Kind {
	case pier.EventWarn:
		fmt.Println(ui.Warn.Render("  !") + " " + e.Message + elapsed)
	case pier.EventNote:
		fmt.Println(ui.Dim.Render("  · "+e.Message) + elapsed)
	default:
		fmt.Println(ui.Step(e.Message) + elapsed)
	}
}

func stepLine(msg string) {
	jobEvents.Write(pier.Event{Kind: pier.EventStep, Message: msg})
	fmt.Println(ui.Step(msg))
}

// --- new session ---------------------------------------------------------------

// --- sessions ---------------------------------------------------------------------

type sessionJSON struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Repository  string     `json:"repository"`
	Branch      string     `json:"branch"`
	Origin      string     `json:"origin"`
	Base        string     `json:"base"`
	Owner       string     `json:"owner"`
	Provider    string     `json:"provider"`
	State       pier.State `json:"state"`
	SetupState  string     `json:"setup_state"`
	Strained    bool       `json:"strained"`
	CreatedAt   *time.Time `json:"created_at"`
	MachineType string     `json:"machine_type"`
	CostNote    string     `json:"cost_note"`
}

// --- repos ------------------------------------------------------------------------

type repoJSON struct {
	Name       string          `json:"name"`
	Image      string          `json:"image"`
	BakedAt    *time.Time      `json:"baked_at"`
	PoolSize   int             `json:"ready_target"`
	Ready      int             `json:"ready"`
	Filling    int             `json:"filling"`
	Sessions   int             `json:"sessions"`
	MonthlyUSD float64         `json:"monthly_usd"`
	Reminders  []pier.Reminder `json:"reminders"`
}

// --- setup / doctor / teardown ------------------------------------------------------

// --- app ----------------------------------------------------------------------------

func cmdTUI() {
	root, _ := pier.RepoRoot("")
	// The app owns the screen: drivers must not write meters or notices into
	// it behind bubbletea's back.
	appOpts := options()
	appOpts.Out, appOpts.Notify = nil, nil
	err := tui.Run(tui.Options{
		Open: func() (tui.Backend, error) {
			c, err := pier.Open(appOpts)
			if err != nil {
				return nil, err // never a typed-nil *Client inside the interface
			}
			return c, nil
		},
		RepoRoot: root,
		Version:  version,
	})
	if errors.Is(err, tui.ErrNoConfig) {
		fmt.Println(ui.Dim.Render("pier isn't set up yet — run `pier setup`"))
		return
	}
	if err != nil {
		fatal(err)
	}
}
