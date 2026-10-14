// Package adapter defines the tool-agnostic schema every per-tool adapter
// (OpenCode, Claude Code, GitHub, Jira, ...) normalizes its events into.
// Nothing in this file should ever import a tool-specific package.
package adapter

import (
	"context"
	"time"
)

// Status is the small set of states the dashboard actually renders.
// Deliberately coarse: fine-grained detail belongs in Message, not here.
type Status string

const (
	StatusRunning    Status = "running"     // agent is actively working
	StatusNeedsInput Status = "needs_input" // agent is blocked on you specifically
	StatusStuck      Status = "stuck"       // agent finished a turn with no clear next step
	StatusDone       Status = "done"        // agent finished the workflow
	StatusFailed     Status = "failed"      // agent errored out
	StatusUnknown    Status = "unknown"     // adapter saw something it doesn't understand yet
)

// Capabilities tells the dashboard what it's allowed to offer for this source.
// Not every source can receive pushed-back input (e.g. a Jira ticket can't).
type Capabilities struct {
	CanReceiveInput bool `json:"can_receive_input"` // true if SendInput below will actually do something
}

// SourceEvent is one normalized update from one adapter about one source.
// This is the only struct that crosses the adapter -> event bus boundary.
type SourceEvent struct {
	Adapter      string       `json:"adapter"`               // "opencode", "github", "jira", ...
	SourceID     string       `json:"source_id"`             // adapter-scoped id, e.g. opencode session id
	WorkflowID   string       `json:"workflow_id,omitempty"` // filled in by the daemon's correlation step, not the adapter
	Status       Status       `json:"status"`                // empty = metadata-only update: refresh Title/Location, leave state alone
	Message      string       `json:"message"`               // short human-readable summary for the dashboard
	Title        string       `json:"title,omitempty"`       // what the source calls itself, e.g. a session title
	Location     string       `json:"location,omitempty"`    // where it lives, e.g. working directory
	PID          int          `json:"pid,omitempty"`         // the agent's OS process, for local sources that know it (liveness, focusing its terminal)
	Parent       string       `json:"parent,omitempty"`      // source ID (same adapter) of the source that spawned this one, e.g. a subagent's parent session
	Timestamp    time.Time    `json:"timestamp"`
	Capabilities Capabilities `json:"capabilities"`
	Ask          *Ask         `json:"ask,omitempty"`      // set when the source became blocked on something the user can answer
	Resolves     string       `json:"resolves,omitempty"` // id of an Ask that was answered or rejected
	Gone         bool         `json:"gone,omitempty"`     // the source no longer exists (e.g. deleted); the daemon drops its workflow
	TurnEnded    bool         `json:"turn_ended,omitempty"`
	Rule         string       `json:"rule,omitempty"` // which done/stuck rule set Status (daemon.Judge), for explaining verdicts // the agent stopped and handed control back; the daemon judges done/stuck/needs_input from a TurnEnd
	Raw          any          `json:"raw,omitempty"`  // original payload, kept for debugging / forward-compat
}

// TurnEnd is the evidence about how an agent's turn ended, read from the
// source when it goes idle. Going idle alone cannot tell "finished" from
// "stuck" from "asked something in plain text"; the daemon's rules
// (daemon.Judge) decide from this. Adapters fill in what their tool exposes
// and leave the rest zero.
type TurnEnd struct {
	Text           string `json:"text,omitempty"`             // the agent's final message text
	Finish         string `json:"finish,omitempty"`           // why generation stopped: "stop" | "length" | "tool-calls" | "content-filter" | other/""
	Error          string `json:"error,omitempty"`            // TurnError* kind, "" if none
	ErrorMessage   string `json:"error_message,omitempty"`    //
	Replied        bool   `json:"replied"`                    // the agent produced anything after the user's last message
	LastToolFailed string `json:"last_tool_failed,omitempty"` // tool whose failure was the last thing that happened (no text after it)
	TodosTotal     int    `json:"todos_total,omitempty"`
	TodosOpen      int    `json:"todos_open,omitempty"` // pending or in progress
}

// TurnEnd.Error kinds.
const (
	TurnErrorAborted         = "aborted" // the user interrupted it
	TurnErrorOutputLength    = "output_length"
	TurnErrorContextOverflow = "context_overflow"
	TurnErrorContentFilter   = "content_filter"
	TurnErrorAuth            = "auth"
	TurnErrorAPI             = "api"
	TurnErrorOther           = "other"
)

// TurnReader is implemented by adapters that can report how a source's last
// turn ended.
type TurnReader interface {
	LastTurn(ctx context.Context, sourceID string) (TurnEnd, error)
}

// AskKind says what sort of request an Ask is.
type AskKind string

const (
	AskKindQuestion   AskKind = "question"   // the agent asked the user something
	AskKindPermission AskKind = "permission" // the agent wants approval to do something
)

// AskQuestion is one question inside an Ask. A permission ask is modelled as a
// single question whose Options are the allowed replies, so every ask is
// answered the same way: one list of chosen strings per question.
type AskQuestion struct {
	Question string   `json:"question"`
	Header   string   `json:"header,omitempty"`
	Options  []string `json:"options,omitempty"`
	Multiple bool     `json:"multiple,omitempty"`  // more than one option may be chosen
	FreeText bool     `json:"free_text,omitempty"` // an answer outside Options is accepted
}

// Ask is a pending request a source is blocked on. ID is adapter-scoped and is
// what the adapter needs to deliver the answer.
type Ask struct {
	ID        string        `json:"id"`
	Kind      AskKind       `json:"kind"`
	Questions []AskQuestion `json:"questions"`
}

// Responder is implemented by adapters whose sources can receive input.
// Pushing input back goes through the same adapter that reported the state,
// because only it knows how to deliver it (API call, pty write, chat send).
type Responder interface {
	// Answer delivers answers (one list per ask question) to a pending ask.
	Answer(ctx context.Context, sourceID string, ask Ask, answers [][]string) error
	// SendText sends free text into the source as if the user had typed it.
	SendText(ctx context.Context, sourceID, text string) error
}
