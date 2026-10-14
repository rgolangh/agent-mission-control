package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"os"
	"time"

	"mission-control/internal/procs"
)

// hookTimeout bounds how long a hook may delay the agent. Claude Code waits
// for hook commands; a daemon that is down or slow must cost next to nothing.
const hookTimeout = 400 * time.Millisecond

// hook is run by an agent's hook system (Claude Code: see
// integrations/claude-code). It reads the hook's JSON payload from stdin,
// adds which process sent it, and posts it to the daemon. It never prints and
// always exits 0: a hook's output and exit status can steer the agent, and
// this one must only ever observe.
//
//	mission-control hook [-addr URL] [-log FILE] claude-code
func hook(args []string) error {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addr := fs.String("addr", "http://"+defaultListen, "mission-control API base URL")
	logFile := fs.String("log", "", "also append each raw payload to this file (debugging)")
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
	if *logFile != "" {
		if f, err := os.OpenFile(*logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			var line bytes.Buffer
			if json.Compact(&line, raw) == nil {
				f.Write(append(line.Bytes(), '\n'))
			}
			f.Close()
		}
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

	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, *addr+"/hooks/claude-code", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: hookTimeout}
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
	return nil
}
