package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"os"
	"time"

	"mission-control/internal/claudecode"
	"mission-control/internal/daemon"
	"mission-control/internal/procs"
)

// hookTimeout bounds how long a hook may delay the agent. Claude Code waits
// for hook commands; a daemon that is down or slow must cost next to nothing.
const hookTimeout = 400 * time.Millisecond

// hook is run by an agent's hook system (Claude Code: see
// integrations/claude-code). It reads the hook's JSON payload from stdin,
// strips it to what the daemon needs (claudecode.Minimize: no prompts beyond
// a title, no file contents, no command output), adds which process sent it,
// and posts it to the daemon's Unix socket. It never prints and always exits
// 0: a hook's output and exit status can steer the agent, and this one must
// only ever observe.
//
//	mission-control hook [-addr ADDR] [-log FILE] claude-code
func hook(args []string) error {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addr := fs.String("addr", daemon.DefaultAddr(), "daemon address")
	logFile := fs.String("log", "", "also append each minimized payload to this file (debugging)")
	if fs.Parse(args) != nil || fs.NArg() != 1 || fs.Arg(0) != "claude-code" {
		return nil
	}

	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	if err != nil || len(raw) == 0 {
		return nil
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}

	// The hook runs as a child (often via a shell) of the claude process.
	// Its PID lets the daemon notice a session that died without a
	// SessionEnd, and find the terminal window it runs in.
	for _, p := range procs.Ancestors(os.Getppid()) {
		if procs.Name(p) == "claude" {
			payload["mc_agent_pid"] = p
			break
		}
	}
	payload = claudecode.Minimize(payload)

	if *logFile != "" {
		if f, err := os.OpenFile(*logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			var line bytes.Buffer
			if b, err := json.Marshal(payload); err == nil && json.Compact(&line, b) == nil {
				f.Write(append(line.Bytes(), '\n'))
			}
			f.Close()
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	_ = daemon.Call(ctx, http.MethodPost, *addr, "/hooks/claude-code", payload, nil)
	return nil
}
