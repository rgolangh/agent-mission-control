// Command mission-control is the workflow state daemon and its CLI.
//
//	mission-control serve  [-listen 127.0.0.1:4200] [-opencode URL]... [-discover]
//	mission-control status [-addr http://127.0.0.1:4200] [-json]
//	mission-control mcp    [-addr http://127.0.0.1:4200]
//
// `serve` subscribes to each OpenCode server given with -opencode (start
// OpenCode with `opencode --port N`, or `opencode serve --port N`), folds the
// normalized events into per-workflow state, and serves that state over HTTP.
// The OpenCode servers do not need to be running yet; serve keeps retrying.
// `status` asks a running daemon for the current state of every workflow.
// `mcp` serves the same state, plus answer/send, as MCP tools on stdio, so an
// orchestrating agent can drive the workflows (see mcp.go).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"mission-control/internal/adapter"
	"mission-control/internal/claudecode"
	"mission-control/internal/daemon"
	"mission-control/internal/discovery"
	"mission-control/internal/opencode"
)

const (
	defaultListen = "127.0.0.1:4200"
	staleness     = 45 * time.Second
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "status":
		err = status(os.Args[2:])
	case "show":
		err = show(os.Args[2:])
	case "answer":
		err = answer(os.Args[2:])
	case "send":
		err = send(os.Args[2:])
	case "mcp":
		err = mcpServe(os.Args[2:])
	case "hook":
		_ = hook(os.Args[2:])
		os.Exit(0) // a hook must never fail the agent
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mission-control:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  mission-control serve  [-listen 127.0.0.1:4200] [-opencode URL]... [-discover]
  mission-control status [-addr http://127.0.0.1:4200] [-json]
  mission-control show   [-addr URL] [-json] <id>            pending asks + history
  mission-control answer [-addr URL] [-ask ID] <id> <choice> answer a pending ask
  mission-control send   [-addr URL] <id> <text...>          send free text
  mission-control mcp    [-addr URL]                         MCP server on stdio (for agents)
<id> is the ID column from status (a unique suffix is enough). <choice> is an
option's number or label; for a permission: once | always | reject.
`)
	os.Exit(2)
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", defaultListen, "address for the mission-control HTTP API")
	password := fs.String("opencode-password", os.Getenv("OPENCODE_SERVER_PASSWORD"), "basic auth password for the OpenCode servers, if they require one")
	var servers stringList
	fs.Var(&servers, "opencode", "OpenCode server base URL (repeatable; default http://localhost:4096)")
	discover := fs.Bool("discover", false, "also find and attach to running `opencode --port N` servers automatically (Linux)")
	discoverEvery := fs.Duration("discover-interval", 3*time.Second, "how often -discover rescans")
	hungAfter := fs.Duration("hung-after", 15*time.Minute, "flag a running workflow as stuck after this long with no activity at all (0 disables)")
	_ = fs.Parse(args)
	if len(servers) == 0 && !*discover {
		servers = stringList{"http://localhost:4096"}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store := daemon.NewStore()
	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "%s [mc] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
	}

	// watch runs one OpenCode adapter until ctx is cancelled. pid is the
	// server's process when discovery found it (0 for -opencode URLs).
	watch := func(ctx context.Context, url string, pid int) {
		client := opencode.New(url)
		client.Password = *password
		onEvent := func(env opencode.Envelope) {
			if sid := opencode.SessionOf(env); sid != "" {
				live := store.WorkflowFor(adapter.SourceEvent{Adapter: "opencode", SourceID: sid})
				if store.Touch(live, time.Now()) {
					logf("%s resumed after being flagged as hung", live)
				}
			}
			ev, ok := opencode.Normalize(env)
			if !ok {
				return
			}
			ev.PID = pid
			id := store.WorkflowFor(ev) // a subagent's events belong to its parent's workflow
			rule := ""
			// A subagent's turn ending is not the workflow's: its idle is dropped anyway.
			if ev.TurnEnded && id == daemon.WorkflowID(ev) {
				if v, err := daemon.JudgeTurn(ctx, client, &ev); err != nil {
					logf("%s:%s reading last turn: %v", ev.Adapter, ev.SourceID, err)
				} else {
					rule = "  (rule: " + v.Rule + ")"
				}
			}
			changed := store.Apply(ev)
			store.Bind(id, client, url) // answer/send route back through this server
			if changed && ev.Gone {
				logf("%s:%s gone — %s", ev.Adapter, ev.SourceID, ev.Message)
			} else if changed {
				wf, _ := store.Get(id)
				logf("%s:%s %s — %s%s%s", ev.Adapter, ev.SourceID, ev.Status, ev.Message, quoted(wf.Title), rule)
			} else if ev.SourceID == "" {
				logf("ignored unattributed event: %s", ev.Message)
			}
		}
		client.SubscribeWithHooks(ctx, staleness, opencode.Hooks{
			OnEvent:      onEvent,
			OnLog:        func(msg string) { logf("%s: %s", url, msg) },
			OnSyncStart:  func() { store.BeginSync(url) },
			OnSynced:     func() { store.EndSync(url, time.Now()) },
			OnDisconnect: func() { store.MarkStale(url, "lost connection to OpenCode at "+url) },
		})
		// Only reached when ctx is cancelled: discovery saw the server go away.
		store.MarkStale(url, "OpenCode at "+url+" went away")
	}

	explicit := map[string]bool{}
	for _, url := range servers {
		explicit[discovery.Canonical(url)] = true
		go watch(ctx, url, 0)
		logf("watching OpenCode at %s", url)
	}

	if *discover {
		go func() {
			active := map[string]context.CancelFunc{} // by discovery.Endpoint.Key()
			for ch := range discovery.New().Watch(ctx, *discoverEvery) {
				ep := ch.Endpoint
				switch ch.Kind {
				case discovery.Added:
					if explicit[discovery.Canonical(ep.URL)] {
						continue // already covered by -opencode
					}
					if ep.AuthRequired && *password == "" {
						logf("found password-protected OpenCode at %s (pid %d) but no -opencode-password; skipping", ep.URL, ep.PID)
						continue
					}
					wctx, cancel := context.WithCancel(ctx)
					active[ep.Key()] = cancel
					logf("discovered OpenCode %s at %s (pid %d)", ep.Version, ep.URL, ep.PID)
					go watch(wctx, ep.URL, ep.PID)
				case discovery.Removed:
					if cancel, ok := active[ep.Key()]; ok {
						cancel()
						delete(active, ep.Key())
						logf("OpenCode at %s (pid %d) went away; stopped watching", ep.URL, ep.PID)
					}
				}
			}
		}()
		logf("discovering OpenCode servers every %s", *discoverEvery)
	}

	if *hungAfter > 0 {
		go func() {
			tick := time.NewTicker(30 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-tick.C:
					for _, id := range store.FlagHung(now, *hungAfter) {
						logf("%s stuck — no activity for over %s", id, *hungAfter)
					}
				}
			}
		}()
	}

	// Claude Code: push-based, through `mission-control hook claude-code`.
	cc := claudecode.New()
	mux := http.NewServeMux()
	mux.Handle("/", daemon.Handler(store))
	mux.Handle("POST /hooks/claude-code", daemon.Guard(claudeHooks(ctx, store, cc, logf)))
	go func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				for id, alive := range cc.Alive() {
					if alive {
						continue
					}
					cc.Forget(id)
					ev := adapter.SourceEvent{Adapter: claudecode.Name, SourceID: id, Gone: true, Status: adapter.StatusDone, Timestamp: time.Now()}
					if store.Apply(ev) {
						logf("%s:%s gone — claude process exited", claudecode.Name, id)
					}
				}
			}
		}
	}()

	srv := &http.Server{Addr: *listen, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	logf("API listening on http://%s (try: mission-control status)", *listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	addr := fs.String("addr", "http://"+defaultListen, "mission-control API base URL")
	asJSON := fs.Bool("json", false, "print raw JSON instead of a table")
	_ = fs.Parse(args)

	workflows, err := daemon.FetchWorkflows(context.Background(), *addr)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(daemon.WorkflowList{Workflows: workflows})
	}
	if len(workflows) == 0 {
		fmt.Println("no workflows yet")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tTITLE\tWHERE\tID\tUPDATED\tMESSAGE")
	for _, w := range workflows {
		st := string(w.Status)
		if w.Stale {
			st += " (stale)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", st, orDash(w.Title), tilde(w.Location), shortID(w.SourceID), age(w.UpdatedAt), w.Message)
	}
	return tw.Flush()
}

func quoted(title string) string {
	if title == "" {
		return ""
	}
	return fmt.Sprintf("  [%s]", title)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// tilde shortens a path under $HOME to ~/..., or "-" if unknown.
func tilde(p string) string {
	if p == "" {
		return "-"
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// shortID keeps the distinguishing tail of a long source id.
func shortID(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

func age(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}
