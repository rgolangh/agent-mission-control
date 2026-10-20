package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	agentmissioncontrol "github.com/rgolangh/agent-mission-control"
	"github.com/rgolangh/agent-mission-control/internal/daemon"
	"github.com/rgolangh/agent-mission-control/internal/install"
	"github.com/rgolangh/agent-mission-control/internal/paths"
)

// version is set at release build time: -ldflags "-X main.version=v0.1.0".
var version = "dev"

func installEnv() (install.Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return install.Env{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return install.Env{}, err
	}
	return install.Env{
		Home:   home,
		Exe:    exe,
		Assets: agentmissioncontrol.Assets,
		Out:    os.Stdout,
		Look:   func(c string) bool { _, err := exec.LookPath(c); return err == nil },
		Run: func(name string, args ...string) error {
			cmd := exec.Command(name, args...)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			return cmd.Run()
		},
	}, nil
}

func installFlags(name string, args []string) (install.Options, error) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	only := fs.String("only", "", "comma-separated components (daemon,opencode,claude-code,omarchy,gnome); default: everything detected")
	dry := fs.Bool("dry-run", false, "show what would change, change nothing")
	force := fs.Bool("force", false, "overwrite (or remove) files not written by agent-mission-control, or changed since")
	enable := fs.Bool("enable", false, "also run the commands that change other tools' settings (enable the shell extension, place the bar widget)")
	_ = fs.Parse(args)
	opt := install.Options{DryRun: *dry, Force: *force, Enable: *enable}
	if *only != "" {
		opt.Only = map[string]bool{}
		for _, c := range strings.Split(*only, ",") {
			opt.Only[strings.TrimSpace(c)] = true
		}
	}
	return opt, nil
}

func installCmd(args []string) error {
	opt, _ := installFlags("install", args)
	e, err := installEnv()
	if err != nil {
		return err
	}
	if err := install.Install(e, version, opt); err != nil {
		return err
	}
	if !opt.DryRun {
		fmt.Println("\ndone. Check everything with: amc doctor")
	}
	return nil
}

func uninstallCmd(args []string) error {
	opt, _ := installFlags("uninstall", args)
	e, err := installEnv()
	if err != nil {
		return err
	}
	return install.Uninstall(e, opt)
}

func versionCmd([]string) error {
	fmt.Println("amc", version)
	return nil
}

// doctor checks the whole chain and says what to do about each problem.
func doctor([]string) error {
	e, err := installEnv()
	if err != nil {
		return err
	}
	problems := 0
	check := func(ok bool, what, fix string) {
		if ok {
			fmt.Printf("  ok    %s\n", what)
			return
		}
		problems++
		fmt.Printf("  FAIL  %s\n        → %s\n", what, fix)
	}
	note := func(what string) { fmt.Printf("  --    %s\n", what) }

	fmt.Printf("amc %s\n\n", version)

	fmt.Println("daemon")
	onPath, _ := exec.LookPath("amc")
	check(onPath != "", "amc is on PATH ("+orDash(onPath)+")", "add ~/.local/bin to PATH (hooks run `amc` by name)")
	check(os.Getenv("XDG_RUNTIME_DIR") != "", "XDG_RUNTIME_DIR is set", "log in through systemd-logind (a normal desktop session); otherwise the socket lives in "+paths.RuntimeDir())
	active := exec.Command("systemctl", "--user", "is-active", "--quiet", "agent-mission-control.service").Run() == nil
	check(active, "service agent-mission-control.service is running", "amc install, or: systemctl --user enable --now agent-mission-control")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	health := map[string]any{}
	err = daemon.Call(ctx, http.MethodGet, daemon.DefaultAddr(), "/healthz", nil, &health)
	check(err == nil, "API answers on "+paths.Socket(), "is the service running? journalctl --user -u agent-mission-control.service")
	if v, ok := health["version"].(string); ok {
		check(v == version, "running daemon is this version ("+v+")", "systemctl --user restart agent-mission-control.service")
	}
	if _, err := paths.ReadSecret(paths.Token()); err == nil {
		note("TCP token present and private (for the desktop widget)")
	} else if !os.IsNotExist(err) {
		check(false, "TCP token file", err.Error())
	}

	fmt.Println("\nintegrations")
	comps, err := install.Plan(e)
	if err != nil {
		return err
	}
	m := e.LoadManifest()
	for _, c := range comps {
		if c.Name == "daemon" || len(c.Files) == 0 {
			continue
		}
		states := map[install.FileState]int{}
		for _, f := range c.Files {
			states[install.State(e.Home, m, f.Path, f.Data)]++
		}
		switch {
		case states[install.Symlink] > 0:
			note(c.Name + ": linked to a checkout (development setup)")
		case states[install.UpToDate] == len(c.Files):
			check(true, c.Name+": installed, matches this version", "")
		case !c.Detected && states[install.Missing] == len(c.Files):
			note(c.Name + ": not installed (" + c.Why + ")")
		case states[install.Missing] == len(c.Files):
			check(false, c.Name+": not installed ("+c.Why+")", "amc install --only "+c.Name)
		default:
			check(false, c.Name+": installed files differ from this version", "amc install (or --force if you changed them)")
		}
	}

	fmt.Println("\nagents")
	if wfs, err := daemon.FetchWorkflows(ctx, daemon.DefaultAddr()); err == nil {
		by := map[string]int{}
		for _, w := range wfs {
			by[w.Adapter]++
		}
		if len(wfs) == 0 {
			note("no workflows yet: start an agent session (opencode / claude) and check again")
		}
		for a, n := range by {
			note(fmt.Sprintf("%s: %d workflow(s)", a, n))
		}
	}

	fmt.Println()
	if problems > 0 {
		return fmt.Errorf("%d problem(s) found", problems)
	}
	fmt.Println("all good")
	return nil
}
