// Package claudecode is the Claude Code adapter. Claude Code has no API to
// subscribe to, so it is push-based: hook commands (installed by the plugin
// in integrations/claude-code) run `mission-control hook claude-code`, which
// posts each hook payload to the daemon, and Adapter.Normalize maps it to a
// SourceEvent. Turn evidence for the done/stuck rules comes from the Stop
// hook's last_assistant_message plus the session's JSONL transcript.
//
// Sources are read-only (CanReceiveInput false): Claude Code is a terminal
// UI with no way to inject an answer, so the dashboard shows what it waits
// for and you answer in the terminal.
//
// Verified against Claude Code 2.1.296 (captured hook payloads and real
// transcripts). Not yet observed live: Notification, PermissionRequest and
// AskUserQuestion payloads, and the interrupt / API-error transcript lines;
// those follow Claude Code's documented formats.
package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
	"github.com/rgolangh/agent-mission-control/internal/procs"
)

// Name is the adapter name: workflows are "claude-code:<session_id>".
const Name = "claude-code"

// maxSessions bounds adapter-only state even if hooks arrive for sessions
// that never produce SessionEnd. The daemon separately bounds workflows.
const maxSessions = 1000

// Hook is the subset of a Claude Code hook payload the adapter reads, plus
// the fields `mission-control hook` adds (mc_*).
type Hook struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Event          string `json:"hook_event_name"`

	Source string `json:"source"` // SessionStart: startup | resume | clear | compact
	Reason string `json:"reason"` // SessionEnd
	Prompt string `json:"prompt"` // UserPromptSubmit

	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	ToolUseID string          `json:"tool_use_id"`

	Message          string `json:"message"`           // Notification
	NotificationType string `json:"notification_type"` // permission_prompt | idle_prompt | ...

	LastAssistantMessage string `json:"last_assistant_message"` // Stop

	AgentPID int `json:"mc_agent_pid"` // the claude process, found by the hook command
}

type session struct {
	transcript   string
	lastText     string // from this turn's Stop hook; "" until it fires
	pid          int
	lastSeen     time.Time
	titled       bool // has a real (ai-title) title; prompts no longer rename it
	titleChecked bool // the transcript was searched for a title at least once
	prompted     bool
}

// Adapter keeps what it has learned about each session between hooks.
type Adapter struct {
	mu       sync.Mutex
	sessions map[string]*session
	// ProjectsDirs are where Claude Code keeps transcripts. A hook's
	// transcript_path is only read if it resolves inside one of them: the
	// path comes from the payload, and must not let a forged hook make the
	// daemon read arbitrary files.
	ProjectsDirs []string
}

func New() *Adapter {
	var dirs []string
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		dirs = append(dirs, filepath.Join(d, "projects"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".claude", "projects"))
	}
	return &Adapter{sessions: map[string]*session{}, ProjectsDirs: dirs}
}

// transcriptPath returns the resolved path if p is a .jsonl file inside one
// of ProjectsDirs (after resolving symlinks on both sides).
func (a *Adapter) transcriptPath(p string) (string, bool) {
	if !filepath.IsAbs(p) || filepath.Ext(p) != ".jsonl" {
		return "", false
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", false
	}
	for _, d := range a.ProjectsDirs {
		root, err := filepath.EvalSymlinks(d)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(root, real); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return real, true
		}
	}
	return "", false
}

var _ adapter.TurnReader = (*Adapter)(nil)

func (a *Adapter) get(id string) *session {
	s, ok := a.sessions[id]
	if !ok {
		s = &session{}
		a.sessions[id] = s
	}
	return s
}

// Normalize maps one hook payload to a SourceEvent. ok is false for hooks
// that carry no workflow state.
func (a *Adapter) Normalize(h Hook) (adapter.SourceEvent, bool) {
	if h.SessionID == "" {
		return adapter.SourceEvent{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.sessions[h.SessionID]; !ok && len(a.sessions) >= maxSessions {
		return adapter.SourceEvent{}, false
	}
	s := a.get(h.SessionID)
	s.lastSeen = time.Now()
	if h.TranscriptPath != "" {
		if p, ok := a.transcriptPath(h.TranscriptPath); ok {
			s.transcript = p
		}
	}
	if h.AgentPID > 0 {
		s.pid = h.AgentPID
	}

	ev := adapter.SourceEvent{
		Adapter:   Name,
		SourceID:  h.SessionID,
		Location:  h.Cwd,
		PID:       s.pid,
		Timestamp: time.Now(),
	}
	// Claude Code writes generated titles (ai-title) into the transcript and
	// refines them; reread at the few points where one may have appeared,
	// never on every tool call.
	// Also on the first hook seen for a session, so sessions that were
	// already running when the daemon (re)started get their title.
	if !s.titleChecked || h.Event == "Stop" || h.Event == "UserPromptSubmit" || h.Event == "SessionStart" {
		s.titleChecked = true
		if t := aiTitle(s.transcript); t != "" {
			ev.Title, s.titled = t, true
		}
	}

	switch h.Event {
	case "SessionStart":
		switch h.Source {
		case "compact":
			ev.Status, ev.Message = adapter.StatusRunning, "context compacted, continuing"
		case "clear":
			ev.Status, ev.Message = adapter.StatusDone, "context cleared, ready for a task"
		default:
			ev.Status, ev.Message = adapter.StatusDone, "ready for a task"
		}

	case "UserPromptSubmit":
		ev.Status, ev.Message = adapter.StatusRunning, "working"
		s.lastText = "" // a new turn
		if !s.titled && !s.prompted && h.Prompt != "" {
			ev.Title = firstLine(h.Prompt, 60) // until Claude Code names the session
		}
		s.prompted = true

	case "PreToolUse":
		ev.Status, ev.Message = adapter.StatusRunning, "working"
		if h.ToolName == "AskUserQuestion" {
			if ask := questionAsk(h); ask != nil {
				ev.Status, ev.Ask = adapter.StatusNeedsInput, ask
				ev.Message = "asked: " + ask.Questions[0].Question
			}
		}

	case "PostToolUse", "PostToolUseFailure":
		ev.Status, ev.Message = adapter.StatusRunning, "working"
		if h.ToolName == "AskUserQuestion" {
			ev.Resolves, ev.Message = h.ToolUseID, "input received, resuming"
		}

	case "PermissionRequest":
		ev.Status = adapter.StatusNeedsInput
		ev.Message = "waiting on approval: " + toolSummary(h)

	case "Notification":
		switch {
		case h.NotificationType == "permission_prompt",
			h.NotificationType == "" && strings.Contains(strings.ToLower(h.Message), "permission"):
			ev.Status, ev.Message = adapter.StatusNeedsInput, "waiting on approval: "+h.Message
		case h.NotificationType == "elicitation_dialog":
			ev.Status, ev.Message = adapter.StatusNeedsInput, "asked: "+h.Message
		case h.NotificationType == "idle_prompt":
			// "Claude is waiting for your input": the turn is over. Stop
			// normally judged it already (same verdict, no change), but an
			// interrupt fires no Stop, so this is how that case gets judged.
			ev.Status, ev.Message, ev.TurnEnded = adapter.StatusDone, "went idle", true
		default:
			return adapter.SourceEvent{}, false
		}

	case "Stop":
		s.lastText = h.LastAssistantMessage
		ev.Status, ev.Message, ev.TurnEnded = adapter.StatusDone, "went idle", true

	case "PreCompact":
		ev.Status, ev.Message = adapter.StatusRunning, "compacting context"

	case "SessionEnd":
		delete(a.sessions, h.SessionID)
		ev.Status, ev.Message, ev.Gone = adapter.StatusDone, "session ended ("+h.Reason+")", true

	default: // SubagentStop and anything new: activity only
		return adapter.SourceEvent{}, false
	}
	return ev, true
}

// Alive reports, for each session whose claude process is known, whether it
// still runs. A closed terminal sends no SessionEnd; this is how the daemon
// notices.
func (a *Adapter) Alive() map[string]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]bool{}
	for id, s := range a.sessions {
		if s.pid > 0 {
			out[id] = procs.Alive(s.pid)
		}
	}
	return out
}

// Forget drops a session's state (after the daemon removed its workflow).
func (a *Adapter) Forget(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, id)
}

// ExpireIdle drops adapter state for old sessions without a live Claude
// process. Live processes are retained even if a long operation emits no
// hooks. Returns the expired session IDs.
func (a *Adapter) ExpireIdle(now time.Time, idle time.Duration) []string {
	if idle <= 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var expired []string
	for id, s := range a.sessions {
		if s.lastSeen.IsZero() || now.Sub(s.lastSeen) < idle || (s.pid > 0 && procs.Alive(s.pid)) {
			continue
		}
		delete(a.sessions, id)
		expired = append(expired, id)
	}
	sort.Strings(expired)
	return expired
}

// LastTurn reads how the session's last turn ended from its transcript, with
// the Stop hook's last_assistant_message as the final text when present.
func (a *Adapter) LastTurn(ctx context.Context, sessionID string) (adapter.TurnEnd, error) {
	a.mu.Lock()
	s, ok := a.sessions[sessionID]
	var path, last string
	if ok {
		path, last = s.transcript, s.lastText
	}
	a.mu.Unlock()
	if path == "" {
		return adapter.TurnEnd{}, fmt.Errorf("no transcript known for %s", sessionID)
	}
	entries, err := readTail(path, tailBytes)
	if err != nil {
		return adapter.TurnEnd{}, err
	}
	t := turnFromTranscript(entries)
	if last != "" {
		// Stop fired for this turn, which it only does when the turn ended
		// normally, and it carries the final text. Trust it over the
		// transcript, which is often not yet flushed when Stop runs (seen
		// live: the final message missing, so the turn looked unanswered).
		// Todos come from earlier tool calls and are already on disk.
		t.Replied, t.Text, t.Finish = true, last, "stop"
		t.Error, t.ErrorMessage, t.LastToolFailed = "", "", ""
	}
	return t, nil
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > n {
		s = strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return s
}

// questionAsk turns an AskUserQuestion tool input
// ({questions: [{question, header, options: [{label, description}], multiSelect}]})
// into an Ask. Claude Code always offers an "Other" free-text choice.
func questionAsk(h Hook) *adapter.Ask {
	var in struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	if json.Unmarshal(h.ToolInput, &in) != nil || len(in.Questions) == 0 {
		return nil
	}
	id := h.ToolUseID
	if id == "" {
		id = h.SessionID + "/question"
	}
	ask := &adapter.Ask{ID: id, Kind: adapter.AskKindQuestion}
	for _, q := range in.Questions {
		aq := adapter.AskQuestion{Question: q.Question, Header: q.Header, Multiple: q.MultiSelect, FreeText: true}
		for _, o := range q.Options {
			aq.Options = append(aq.Options, o.Label)
		}
		ask.Questions = append(ask.Questions, aq)
	}
	return ask
}

// toolSummary is "Bash: ls -la" style shorthand for a tool call.
func toolSummary(h Hook) string {
	var in map[string]any
	_ = json.Unmarshal(h.ToolInput, &in)
	for _, k := range []string{"command", "file_path", "url", "pattern", "path"} {
		if v, ok := in[k].(string); ok && v != "" {
			return h.ToolName + ": " + firstLine(v, 80)
		}
	}
	if h.ToolName == "" {
		return "a tool call"
	}
	return h.ToolName
}
