package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/daemon"
)

// parseFlags parses fs and returns the remaining positional args. Flags must
// come before positionals (standard Go flag behaviour).
func parseFlags(fs *flag.FlagSet, args []string) []string {
	_ = fs.Parse(args)
	return fs.Args()
}

func show(args []string) error {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	addr := fs.String("addr", daemon.DefaultAddr(), "daemon address: unix:<socket> (default) or http://host:port (reads the token file)")
	asJSON := fs.Bool("json", false, "print raw JSON")
	pos := parseFlags(fs, args)
	if len(pos) != 1 {
		return fmt.Errorf("usage: mission-control show [-addr URL] [-json] <id>")
	}
	wf, err := daemon.FetchWorkflow(context.Background(), *addr, pos[0])
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(wf)
	}

	fmt.Printf("%s\n", wf.ID)
	fmt.Printf("  title    %s\n", orDash(wf.Title))
	fmt.Printf("  where    %s\n", tilde(wf.Location))
	fmt.Printf("  status   %s (%s)\n", wf.Status, age(wf.UpdatedAt))
	fmt.Printf("  message  %s\n", wf.Message)
	if wf.Stale {
		fmt.Printf("  stale    %s — last known state, may be out of date; input is refused\n", wf.StaleReason)
	}
	if !wf.Capabilities.CanReceiveInput {
		fmt.Println("  input    this source cannot receive input")
	}

	for _, a := range wf.Pending {
		fmt.Printf("\nwaiting on you: %s %s\n", a.Kind, a.ID)
		for i, q := range a.Questions {
			label := q.Question
			if q.Header != "" {
				label = q.Header + " — " + q.Question
			}
			fmt.Printf("  %s\n", label)
			for j, o := range q.Options {
				fmt.Printf("    %d) %s\n", j+1, o)
			}
			switch {
			case q.Multiple:
				fmt.Println("    (several allowed: comma-separated)")
			case q.FreeText:
				fmt.Println("    (or type your own answer)")
			}
			if i == 0 && len(a.Questions) == 1 {
				fmt.Printf("  answer with: mission-control answer %s <choice>\n", shortID(wf.SourceID))
			}
		}
		if len(a.Questions) > 1 {
			fmt.Println("  (several questions: use the API's `answers` field)")
		}
	}

	fmt.Println("\nhistory")
	for _, h := range wf.History {
		fmt.Printf("  %s  %-11s  %s\n", h.At.Local().Format("15:04:05"), h.Status, h.Message)
	}
	return nil
}

func answer(args []string) error {
	fs := flag.NewFlagSet("answer", flag.ExitOnError)
	addr := fs.String("addr", daemon.DefaultAddr(), "daemon address: unix:<socket> (default) or http://host:port (reads the token file)")
	askID := fs.String("ask", "", "ask ID, only needed when several asks are pending")
	pos := parseFlags(fs, args)
	if len(pos) < 2 {
		return fmt.Errorf("usage: mission-control answer [-addr URL] [-ask ID] <id> <choice>")
	}
	ref, choice := pos[0], strings.Join(pos[1:], " ")

	ctx := context.Background()
	res, err := daemon.PostAnswer(ctx, *addr, ref, daemon.AnswerRequest{AskID: *askID, Choice: choice})
	if err != nil {
		return err
	}
	fmt.Printf("sent %v to %s\n", flatten(res.Answers), res.WorkflowID)

	// State only moves when the source confirms, so wait briefly for that
	// rather than claiming success we haven't seen.
	if wf, ok := daemon.AwaitResolved(ctx, *addr, res.WorkflowID, res.AskID, 6*time.Second); ok {
		fmt.Printf("confirmed: now %s — %s\n", wf.Status, wf.Message)
		return nil
	}
	fmt.Println("delivered, but no confirmation from the source yet; check `mission-control show`")
	return nil
}

func send(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	addr := fs.String("addr", daemon.DefaultAddr(), "daemon address: unix:<socket> (default) or http://host:port (reads the token file)")
	pos := parseFlags(fs, args)
	if len(pos) < 2 {
		return fmt.Errorf("usage: mission-control send [-addr URL] <id> <text...>")
	}
	if err := daemon.PostSend(context.Background(), *addr, pos[0], strings.Join(pos[1:], " ")); err != nil {
		return err
	}
	fmt.Println("sent")
	return nil
}

func flatten(answers [][]string) []string {
	var out []string
	for _, a := range answers {
		out = append(out, strings.Join(a, ", "))
	}
	return out
}
