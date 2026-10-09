package main

// Commands on pier's own setup: setup, skills, doctor, teardown.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/ui"
	"github.com/usepier/pier/internal/wizard"
	"github.com/usepier/pier/pkg/pier"
)

func cmdSetup(args []string) {
	printAdmin := false
	for _, a := range args {
		switch a {
		case "--print-admin":
			printAdmin = true
		case "--skills":
			cmdSkills()
			return
		default:
			fatal(fmt.Errorf("usage: pier setup [--print-admin | --skills]"))
		}
	}
	if err := wizard.Run(options(), printAdmin); err != nil {
		fatal(err)
	}
}

// cmdSkills refreshes the bundled skills for every agent on the machine,
// e.g. after a pier upgrade. Idempotent; a no-op prints as such.
func cmdSkills() {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal(err)
	}
	agents := pier.AgentDirs(home)
	if len(agents) == 0 {
		fmt.Println(ui.Dim.Render("  no agent config found (~/.claude or ~/.codex) — nothing to install into"))
		return
	}
	notes, err := wizard.InstallSkills(home, agents)
	for _, n := range notes {
		fmt.Println(n)
	}
	if err != nil {
		fatal(err)
	}
}

func cmdDoctor(jsonOutput bool) {
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		note := ui.Warn.Render("!") + ui.Dim.Render(" no config yet — checking with defaults (run `pier setup`)")
		if jsonOutput {
			fmt.Fprintln(os.Stderr, note)
		} else {
			fmt.Println(note)
		}
		cfg = config.Default()
	}
	c, err := pier.New(cfg, options())
	if err != nil {
		fatal(err)
	}
	var checks []pier.Check
	if cfgErr == nil {
		checks = c.Doctor(context.Background())
	} else {
		checks = c.Driver().Doctor(context.Background())
	}
	if jsonOutput {
		type check struct {
			Name   string `json:"name"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail,omitempty"`
			Fix    string `json:"fix,omitempty"`
		}
		out := []check{}
		allOK := true
		for _, ch := range checks {
			out = append(out, check{ch.Name, ch.OK, ch.Detail, ch.Fix})
			allOK = allOK && ch.OK
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		if !allOK {
			os.Exit(1)
		}
		return
	}
	if !printChecks(checks) {
		os.Exit(1)
	}
}

func printChecks(checks []pier.Check) bool {
	allOK := true
	for _, c := range checks {
		allOK = allOK && c.OK
		line := "  " + ui.Mark(c.OK) + " " + c.Name
		if c.Detail != "" {
			line += ui.Dim.Render(" — " + c.Detail)
		}
		fmt.Println(line)
	}
	return allOK
}

func cmdTeardown() {
	c := open()
	if !confirm("remove all pier groundwork, images and pooled sessions from the account?", false) {
		return
	}
	if err := c.Teardown(context.Background(), func(e pier.Event) { stepLine(e.Message) }); err != nil {
		fatal(err)
	}
	fmt.Println(ui.OK.Render("groundwork removed — the account is clean"))
}

func confirm(prompt string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	fmt.Printf("%s [%s] ", prompt, hint)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return def
	}
	return line == "y" || line == "yes"
}
