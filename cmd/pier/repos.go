package main

// Commands on repositories: the list, images, pools.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/usepier/pier/internal/ui"
	"github.com/usepier/pier/pkg/pier"
)

func cmdRepos(args []string) {
	jsonOutput := len(args) > 0 && args[0] == "--json"
	c := open()
	sessions, err := c.Sessions(context.Background())
	if err != nil {
		fatal(err)
	}
	cur, _ := pier.RepoRoot("")
	repos := c.Repos(sessions, cur)
	if jsonOutput {
		items := make([]repoJSON, 0, len(repos))
		for _, r := range repos {
			j := repoJSON{Name: r.Name, Image: r.Image, PoolSize: r.PoolSize, Ready: r.Ready,
				Filling: r.Filling, Sessions: r.Sessions, MonthlyUSD: r.MonthlyUSD, Reminders: r.Reminders}
			if r.Baked != nil {
				t := r.Baked.BakedAt
				j.BakedAt = &t
			}
			items = append(items, j)
		}
		if err := json.NewEncoder(os.Stdout).Encode(items); err != nil {
			fatal(err)
		}
		return
	}
	if len(repos) == 0 {
		fmt.Println(ui.Dim.Render("no repos yet — `pier <branch>` inside one starts a session"))
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "REPO\tIMAGE\tPOOL\tSESSIONS\tIDLE COST")
	for _, r := range repos {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", r.Name, imageLabel(r), poolLabel(r), r.Sessions, money(r.MonthlyUSD))
	}
	w.Flush()
	for _, r := range repos {
		for _, rem := range r.Reminders {
			fmt.Println(ui.Accent.Render("  tip") + " " + rem.Message + ui.Dim.Render(" `"+rem.Action+"`"))
		}
	}
}

func imageLabel(r pier.Repo) string {
	switch {
	case r.Image == "":
		return "none"
	case r.Baked == nil:
		return "baked"
	}
	kind := "prebuilt"
	if !r.Baked.RepoIncluded {
		kind = "toolchains"
	}
	if a := pier.Age(r.Baked.BakedAt, time.Now()); a != "now" {
		return kind + ", " + a + " old"
	}
	return kind + ", just baked"
}

func poolLabel(r pier.Repo) string {
	if r.PoolSize == 0 && r.Ready == 0 && r.Filling == 0 {
		return "off"
	}
	l := fmt.Sprintf("%d/%d", r.Ready, r.PoolSize)
	if r.Filling > 0 {
		l += fmt.Sprintf(" (+%d filling)", r.Filling)
	}
	return l
}

func money(usd float64) string {
	if usd == 0 {
		return "$0"
	}
	return fmt.Sprintf("~$%.0f/mo", usd)
}

func cmdBake(args []string) {
	var include *bool
	for _, a := range args {
		switch a {
		case "--finish":
			// Hidden: a bake's background half, spawned by the bake itself.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := open().FinishBake(ctx, repoRoot(), progress); err != nil {
				fatal(err)
			}
			return
		case "--toolchain-only":
			f := false
			include = &f
		case "--with-repo":
			t := true
			include = &t
		default:
			fatal(fmt.Errorf("usage: pier bake [--toolchain-only | --with-repo]"))
		}
	}
	c := open()
	root := repoRoot() // images are repo-specific: bake from inside the repo it serves
	name := filepath.Base(root)
	withRepo := c.Config().Speed.ImageRepo
	if include != nil {
		withRepo = *include
	}
	what := "harnesses ~5 min, then one full .pier/setup.sh run"
	if !withRepo {
		what = "harnesses and .pier/bake.sh toolchains, ~5 min"
	}
	fmt.Printf("%s %s\n", ui.Bold.Render("baking "+name), ui.Dim.Render("("+what+" on a temporary instance, then an image — ~$1-3/mo storage)"))
	if withRepo {
		stepLine("prebuilding from HEAD — the checkout and setup's artifacts go into the image; every secret pier pushed is scrubbed first")
	}
	// ctrl-c mid-bake must cancel the ctx (not just kill the process) so
	// the bake's cleanup can terminate the temporary instance — it has no
	// supervisor, so a leaked one never parks itself.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := c.Bake(ctx, pier.BakeRequest{RepoRoot: root, IncludeRepo: include, Progress: progress})
	if err != nil {
		fatal(err)
	}
	if res.FinishLog == "" {
		// The finish couldn't go to the background and ran here instead.
		fmt.Println(ui.OK.Render("baked "+res.Image) + ui.Dim.Render(" — new "+name+" sessions start from it"))
		return
	}
	fmt.Println(ui.OK.Render("baked "+name) + ui.Dim.Render(" — image "+res.Image+" is being saved in the background; new sessions start from it once it's saved, and the pool fills from it then"))
	fmt.Println(ui.Dim.Render("log: " + ui.Tilde(res.FinishLog)))
}

// cmdPool: this repo's pool — parked, setup-complete sessions `pier <branch>`
// claims instead of launching. `pier pool` shows it; set/fill/drain change it.
// Fills also happen by themselves after setup, every bake, every create and
// every size change.
func cmdPool(args []string) {
	if len(args) == 0 {
		poolStatus()
		return
	}
	switch args[0] {
	case "set":
		if len(args) != 2 {
			fatal(fmt.Errorf("usage: pier pool set <n>   (0-8; 0 turns the pool off and removes its sessions)"))
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 || n > 8 {
			fatal(fmt.Errorf("pool size: want a number 0-8 (got %q)", args[1]))
		}
		c := open()
		root := repoRoot()
		logPath, err := c.SetPoolSize(context.Background(), root, n, func(e pier.Event) { stepLine(e.Message) })
		if err != nil {
			fatal(err)
		}
		repo := filepath.Base(root)
		if n == 0 {
			fmt.Println(ui.OK.Render("pool off for " + repo))
			return
		}
		fmt.Println(ui.OK.Render(fmt.Sprintf("pool size %d for %s", n, repo)) +
			ui.Dim.Render(fmt.Sprintf(" — each pooled session ~$%.0f/mo parked (disk only)", c.DiskMonthlyUSD())))
		fmt.Println(ui.Dim.Render("filling in the background — log: " + ui.Tilde(logPath)))
	case "fill":
		detach := len(args) > 1 && (args[1] == "-d" || args[1] == "--detach")
		c := open()
		root := repoRoot()
		if c.Config().PoolSize(filepath.Base(root)) == 0 {
			fatal(fmt.Errorf("no pool for %s — `pier pool set <n>` first", filepath.Base(root)))
		}
		if detach {
			logPath, err := c.SpawnFill(root)
			if err != nil {
				fatal(err)
			}
			fmt.Println(ui.Dim.Render("filling in the background — log: " + ui.Tilde(logPath)))
			return
		}
		// ctrl-c must cancel the ctx so a half-made member is terminated.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := c.FillPool(ctx, root, progress); err != nil {
			fatal(err)
		}
	case "drain":
		c := open()
		repo := ""
		if len(args) > 1 {
			repo = args[1]
		} else {
			repo = filepath.Base(repoRoot())
		}
		n, err := c.DrainPool(context.Background(), repo, func(e pier.Event) { stepLine(e.Message) })
		if err != nil {
			fatal(err)
		}
		if n == 0 {
			fmt.Println(ui.Dim.Render("no pooled sessions for " + repo))
			return
		}
		fmt.Println(ui.OK.Render(fmt.Sprintf("drained %d pooled session(s) from %s", n, repo)))
		if size := c.Config().PoolSize(repo); size > 0 {
			fmt.Println(ui.Dim.Render(fmt.Sprintf("pool size for %s is still %d — the next create or `pier pool fill` refills it; `pier pool set 0` turns it off", repo, size)))
		}
	default:
		fatal(fmt.Errorf("usage: pier pool [set <n> | fill [-d] | drain [repo]]"))
	}
}

// poolStatus prints every repo's pool — this repo's first — with its cost.
func poolStatus() {
	c := open()
	sessions, err := c.Sessions(context.Background())
	if err != nil {
		fatal(err)
	}
	cur, _ := pier.RepoRoot("")
	any := false
	for _, r := range c.Repos(sessions, cur) {
		if r.PoolSize == 0 && r.Ready+r.Filling+r.Stale == 0 {
			if r.Current {
				fmt.Println(r.Name + ": no pool" + ui.Dim.Render(poolHint(r)))
			}
			continue
		}
		any = true
		line := fmt.Sprintf("%s: %d/%d ready", r.Name, r.Ready, r.PoolSize)
		if r.Filling > 0 {
			line += fmt.Sprintf(", %d filling", r.Filling)
		}
		if r.Stale > 0 {
			line += ui.Warn.Render(fmt.Sprintf(", %d stale", r.Stale)) + ui.Dim.Render(" (recycle on the next fill)")
		}
		if r.Ready+r.Filling > 0 {
			line += ui.Dim.Render(fmt.Sprintf(" — ~$%.0f/mo parked", float64(r.Ready+r.Filling)*c.DiskMonthlyUSD()))
		}
		fmt.Println(line)
	}
	if !any && cur == "" {
		fmt.Println(ui.Dim.Render("no pools — `pier pool set <n>` inside a repo keeps set-up sessions ready to claim"))
	}
}

func poolHint(r pier.Repo) string {
	if r.Image == "" {
		return " — pools follow the speed settings for repos with a session image; `pier bake` first"
	}
	return " — `pier pool set <n>` keeps set-up sessions ready to claim"
}
