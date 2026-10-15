package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
	"github.com/rgolangh/agent-mission-control/internal/claudecode"
	"github.com/rgolangh/agent-mission-control/internal/daemon"
)

// judgeCmd runs the done/stuck rules on a message or a transcript, so a
// verdict can be reproduced, explained and turned into a test case.
//
//	mission-control judge [-all] [-only RULES] <transcript.jsonl | file | ->
func judgeCmd(args []string) error {
	fs := flag.NewFlagSet("judge", flag.ExitOnError)
	all := fs.Bool("all", false, "transcript: judge every turn, not just the last")
	only := fs.String("only", "", "comma-separated rules or statuses to show (e.g. stuck,question)")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: mission-control judge [-all] [-only RULES] <transcript.jsonl | text file | ->")
	}
	src := fs.Arg(0)
	keep := func(v daemon.Verdict) bool {
		if *only == "" {
			return true
		}
		for _, f := range strings.Split(*only, ",") {
			if f = strings.TrimSpace(f); f == v.Rule || f == string(v.Status) {
				return true
			}
		}
		return false
	}

	if strings.HasSuffix(src, ".jsonl") {
		turns, err := claudecode.TurnsFromFile(src)
		if err != nil {
			return err
		}
		if !*all && len(turns) > 0 {
			turns = turns[len(turns)-1:]
		}
		counts := map[string]int{}
		for i, t := range turns {
			v := daemon.Judge(t.End)
			counts[string(v.Status)]++
			if !keep(v) {
				continue
			}
			fmt.Printf("#%-3d %-11s %-16s %s\n", i+1, v.Status, v.Rule, oneLine(v.Message, 110))
			fmt.Printf("     prompt: %s\n", oneLine(t.Prompt, 100))
		}
		if *all {
			fmt.Printf("\n%d turns: %v\n", len(turns), counts)
		}
		return nil
	}

	var b []byte
	var err error
	if src == "-" {
		b, err = io.ReadAll(os.Stdin)
	} else {
		b, err = os.ReadFile(src)
	}
	if err != nil {
		return err
	}
	v := daemon.Judge(adapter.TurnEnd{Text: string(b), Finish: "stop", Replied: true})
	fmt.Printf("%s  (rule: %s)\n%s\n", v.Status, v.Rule, adapter.CleanText(v.Message))
	return nil
}

// oneLine flattens agent text for a table cell, without control characters.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(adapter.CleanText(s)), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
