package opencode

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

// Envelope is the wire shape of every frame OpenCode sends on its event
// stream: { id, type, properties } on v1, { id, type, data } on v2 —
// UnmarshalJSON maps both onto Properties so the rest of the adapter is
// version-agnostic. The extra top-level `id` is ignored.
//
// Everything below was verified against live servers (v1.18.30, v2.2.5):
// the type strings and property shapes come from their OpenAPI specs, and
// the lifecycle frames were observed on the real event stream. These are
// still internal event names, not a published contract — they change between
// releases, so re-verify after upgrading OpenCode.
type Envelope struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`

	// API is the server's major API version (1 or 2); the subscriber sets it
	// so Normalize can read version-specific shapes. 0 means v1.
	API int `json:"-"`
}

// UnmarshalJSON accepts both wire shapes: v2 frames carry the payload under
// `data`, v1 frames under `properties`.
func (e *Envelope) UnmarshalJSON(b []byte) error {
	type wire struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
		Data       json.RawMessage `json:"data"`
	}
	var w wire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	e.Type, e.Properties = w.Type, w.Properties
	if e.Properties == nil {
		e.Properties = w.Data
	}
	return nil
}

const (
	EventSessionCreated   = "session.created"
	EventSessionUpdated   = "session.updated"
	EventSessionDeleted   = "session.deleted"
	EventSessionStatus    = "session.status"
	EventSessionIdle      = "session.idle"
	EventSessionError     = "session.error"
	EventTodoUpdated      = "todo.updated"
	EventPermissionAsked  = "permission.asked"
	EventPermissionReply  = "permission.replied"
	EventQuestionAsked    = "question.asked"
	EventQuestionReplied  = "question.replied"
	EventQuestionRejected = "question.rejected"

	// v2 event names (the shared service's stream).
	EventV2SessionRenamed     = "session.renamed"
	EventV2SessionMoved       = "session.moved"
	EventV2ExecutionStarted   = "session.execution.started"
	EventV2ExecutionSucceeded = "session.execution.succeeded"
	EventV2ExecutionFailed    = "session.execution.failed"
	EventV2ExecutionInterrupt = "session.execution.interrupted"
	EventV2RetryScheduled     = "session.retry.scheduled"
	EventV2FormCreated        = "form.created"
	EventV2FormReplied        = "form.replied"
	EventV2FormCancelled      = "form.cancelled"
)

// retriesBeforeStuck is the retry attempt at which a session counts as stuck
// on its provider rather than briefly rate limited.
const retriesBeforeStuck = 3

// noisy lists high-volume frames that carry no workflow-level state. They are
// dropped by Normalize; anything NOT listed here and not handled below is
// still surfaced as StatusUnknown so new event types stay visible. The v2
// additions are the per-step streaming and bookkeeping families the shared
// service emits constantly.
var noisy = map[string]bool{
	"server.connected":     true,
	"server.heartbeat":     true, // synthesized locally from SSE comment lines
	"plugin.added":         true,
	"catalog.updated":      true,
	"integration.updated":  true,
	"reference.updated":    true,
	"message.updated":      true,
	"message.part.updated": true,
	"message.part.delta":   true,
	"session.diff":         true,
	"file.edited":          true,
	"file.watcher.updated": true,

	// v2: high-volume, no workflow-level state.
	"models-dev.refreshed":           true,
	"credential.updated":             true,
	"credential.switched":            true,
	"provider.updated":               true,
	"model.updated":                  true,
	"agent.updated":                  true,
	"command.updated":                true,
	"skill.updated":                  true,
	"config.updated":                 true,
	"plugin.updated":                 true,
	"project.updated":                true,
	"websearch.updated":              true,
	"installation.updated":           true,
	"installation.update-available":  true,
	"vcs.branch.updated":             true,
	"mcp.status.changed":             true,
	"mcp.resources.changed":          true,
	"worktree.updated":               true,
	"worktree.resolved":              true,
	"artifact.updated":               true,
	"filesystem.changed":             true,
	"location.shutdown":              true,
	"shell.created":                  true,
	"shell.exited":                   true,
	"shell.deleted":                  true,
	"pty.created":                    true,
	"pty.updated":                    true,
	"pty.exited":                     true,
	"pty.deleted":                    true,
	"persistent-pty.added":           true,
	"persistent-pty.removed":         true,
	"persistent-pty.updated":         true,
	"session.usage.updated":          true,
	"session.viewed":                 true,
	"session.archived":               true,
	"session.unarchived":             true,
	"session.permissions":            true,
	"session.metadata.updated":       true,
	"session.instructions.updated":   true,
	"session.agent.selected":         true,
	"session.model.selected":         true,
	"session.inbox.enqueued":         true,
	"session.inbox.delivered":        true,
	"session.inbox.cancelled":        true,
	"session.inbox.delivery.changed": true,
	"session.synthetic":              true,
	"session.skill.activated":        true,
	"session.shell.started":          true,
	"session.shell.ended":            true,
	"session.step.started":           true,
	"session.step.streamed":          true,
	"session.step.ended":             true,
	"session.step.failed":            true,
	"session.step.first-token":       true,
	"session.text.started":           true,
	"session.text.delta":             true,
	"session.text.ended":             true,
	"session.reasoning.started":      true,
	"session.reasoning.delta":        true,
	"session.reasoning.ended":        true,
	"session.tool.called":            true,
	"session.tool.input.started":     true,
	"session.tool.input.delta":       true,
	"session.tool.input.ended":       true,
	"session.tool.progress":          true,
	"session.tool.success":           true,
	"session.tool.failed":            true,
	"session.compaction.started":     true,
	"session.compaction.delta":       true,
	"session.compaction.ended":       true,
	"session.compaction.failed":      true,
	"session.revert.staged":          true,
	"session.revert.cleared":         true,
	"session.revert.committed":       true,
}

// SessionProps is the common shape of session.idle and friends.
type SessionProps struct {
	SessionID string `json:"sessionID"`
}

// SessionInfoProps: session.created / session.updated carry the session's
// metadata under `info`. OpenCode creates sessions titled "New session - …"
// and replaces the title with a generated one shortly after the first prompt.
type SessionInfoProps struct {
	SessionID string `json:"sessionID"`
	Info      struct {
		Title     string `json:"title"`
		Directory string `json:"directory"`
		ParentID  string `json:"parentID"` // set on subagent (task tool) sessions
	} `json:"info"`
}

// StatusProps: session.status carries a tagged union on status.type, one of
// "busy" | "idle" | "retry" (retry adds attempt/message).
type StatusProps struct {
	SessionID string `json:"sessionID"`
	Status    struct {
		Type    string `json:"type"`
		Attempt int    `json:"attempt"`
		Message string `json:"message"`
	} `json:"status"`
}

// ErrorProps: session.error. Both fields are optional in the spec.
type ErrorProps struct {
	SessionID string `json:"sessionID"`
	Error     struct {
		Name string          `json:"name"`
		Data json.RawMessage `json:"data"`
	} `json:"error"`
}

// Todo mirrors OpenCode's todo item. Status is one of
// pending | in_progress | completed | cancelled.
type Todo struct {
	Content  string `json:"content"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
}

type TodoUpdatedProps struct {
	SessionID string `json:"sessionID"`
	Todos     []Todo `json:"todos"`
}

// PermissionAskedProps: permission.asked. `ID` is the request ID to pass to
// ResolvePermission; `Permission` is the kind of access (e.g. "bash",
// "edit") and `Patterns` what it applies to.
type PermissionAskedProps struct {
	ID         string   `json:"id"`
	SessionID  string   `json:"sessionID"`
	Permission string   `json:"permission"`
	Patterns   []string `json:"patterns"`
}

// ReplyProps is the shared shape of permission.replied / question.replied /
// question.rejected: the resolved request's ID plus the session it was in.
type ReplyProps struct {
	SessionID string `json:"sessionID"`
	RequestID string `json:"requestID"`
}

// QuestionAskedProps: question.asked — the agent called its question tool
// and is blocked until answered. `ID` is the request ID for ReplyQuestion.
type QuestionAskedProps struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Questions []struct {
		Question string `json:"question"`
		Header   string `json:"header"`
		Options  []struct {
			Label string `json:"label"`
		} `json:"options"`
		Multiple bool  `json:"multiple"`
		Custom   *bool `json:"custom"` // typed answer allowed; absent is treated as allowed
	} `json:"questions"`
}

// V2CreatedProps: session.created on v2. The title starts empty and is
// replaced (session.renamed) shortly after the first prompt; slug is the
// best label until then. parentID marks subagent (task tool) sessions.
type V2CreatedProps struct {
	SessionID string `json:"sessionID"`
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	ParentID  string `json:"parentID"`
	Location  struct {
		Directory string `json:"directory"`
	} `json:"location"`
}

// V2RenamedProps: session.renamed on v2. Live frames carry only the title;
// the snapshot synthesizes renamed frames with Directory and ParentID too,
// so one metadata frame can carry everything the daemon labels a workflow
// with. Directory is decoded from the frame's own location only when
// present.
type V2RenamedProps struct {
	SessionID string `json:"sessionID"`
	Title     string `json:"title"`

	Directory string `json:"directory"`
	ParentID  string `json:"parentID"`
}

// V2ExecutionProps: session.execution.* on v2 — the turn lifecycle.
// execution.failed adds an error {type, message}.
type V2ExecutionProps struct {
	SessionID string `json:"sessionID"`
	Error     *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// V2PermissionAskedProps: permission.asked on v2. ID plus SessionID are what
// ResolvePermission needs; Action and Resources describe the access.
type V2PermissionAskedProps struct {
	ID        string   `json:"id"`
	SessionID string   `json:"sessionID"`
	Action    string   `json:"action"`
	Resources []string `json:"resources"`
	Message   string   `json:"message"`
}

// V2PermissionRepliedProps: permission.replied on v2. Superset of the v1
// shape (which has no reply field).
type V2PermissionRepliedProps struct {
	SessionID string `json:"sessionID"`
	RequestID string `json:"requestID"`
	Reply     string `json:"reply"`
}

// V2FormField is one field of a v2 form: the replacement for v1 questions.
// A field with Options is answered by choosing among them (multiselect
// allows several); Custom allows a typed answer.
type V2FormField struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Type        string `json:"type"` // string | number | integer | boolean | multiselect | external
	Custom      bool   `json:"custom"`
	Options     []struct {
		Value       string `json:"value"`
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
}

// V2FormProps: form.created on v2. The agent's question tool creates a form
// and blocks until it is answered or cancelled.
type V2FormProps struct {
	Form struct {
		ID        string        `json:"id"`
		SessionID string        `json:"sessionID"`
		Title     string        `json:"title"`
		Fields    []V2FormField `json:"fields"`
	} `json:"form"`
}

// V2FormSettledProps: form.replied / form.cancelled on v2. ID is the form's;
// Answer is only present on replied and is not needed to resolve the ask.
type V2FormSettledProps struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
}

// V2RetryProps: session.retry.scheduled on v2 — a step failed and will be
// retried (rate limits, overload).
type V2RetryProps struct {
	SessionID string `json:"sessionID"`
	Attempt   int    `json:"attempt"`
	Error     struct {
		Message string `json:"message"`
	} `json:"error"`
}

// V2MovedProps: session.moved on v2 — the session changed directory.
type V2MovedProps struct {
	SessionID string `json:"sessionID"`
	Location  struct {
		Directory string `json:"directory"`
	} `json:"location"`
}

// SessionOf returns the session a frame is about, for every frame type that
// has one (all session-scoped frames carry a top-level sessionID, including
// the noisy ones Normalize drops), or "".
func SessionOf(env Envelope) string {
	var p SessionProps
	_ = json.Unmarshal(env.Properties, &p)
	return p.SessionID
}

// Normalize turns one raw Envelope into our tool-agnostic SourceEvent. The
// bool is false for frames that are known noise and should not be published.
// Request IDs needed to answer a question/permission live in Raw (the
// original envelope) so the daemon can hand them back to this adapter.
//
// A note on session.status: "busy" does NOT mean the agent is making
// progress. While blocked on a question or permission ask the session still
// reports busy, so the ask events (needs_input) are the source of truth for
// that state and the daemon must not let a later "busy" frame overwrite them
// until the matching *.replied / *.rejected arrives.
func Normalize(env Envelope) (adapter.SourceEvent, bool) {
	if noisy[env.Type] {
		return adapter.SourceEvent{}, false
	}

	ev := adapter.SourceEvent{
		Adapter:      "opencode",
		Timestamp:    time.Now(),
		Capabilities: adapter.Capabilities{CanReceiveInput: true},
		Raw:          env,
	}

	switch env.Type {
	case EventSessionCreated:
		if env.API == 2 {
			var p V2CreatedProps
			_ = json.Unmarshal(env.Properties, &p)
			ev.SourceID, ev.Status, ev.Message = p.SessionID, adapter.StatusRunning, "session started"
			ev.Title = p.Title
			if ev.Title == "" {
				ev.Title = p.Slug // the generated title arrives with session.renamed
			}
			ev.Location, ev.Parent = p.Location.Directory, p.ParentID
			break
		}
		var p SessionInfoProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID, ev.Status, ev.Message = p.SessionID, adapter.StatusRunning, "session started"
		ev.Title, ev.Location, ev.Parent = p.Info.Title, p.Info.Directory, p.Info.ParentID

	case EventV2SessionRenamed:
		// Metadata only (retitle): no Status, so the daemon refreshes the
		// label without touching state or history.
		var p V2RenamedProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID = p.SessionID
		ev.Title = p.Title
		ev.Location, ev.Parent = p.Directory, p.ParentID // set by the snapshot's synthesized frames

	case EventV2SessionMoved:
		var p V2MovedProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID = p.SessionID
		ev.Location = p.Location.Directory

	case EventSessionUpdated:
		// Metadata only (retitle, cost, summary…): no Status, so the daemon
		// refreshes the label without touching state or history. v1 only;
		// v2 has session.renamed and session.moved above.
		var p SessionInfoProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID = p.SessionID
		ev.Title, ev.Location, ev.Parent = p.Info.Title, p.Info.Directory, p.Info.ParentID

	case EventSessionDeleted:
		var p SessionProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID, ev.Status, ev.Gone = p.SessionID, adapter.StatusDone, true
		ev.Message = "session deleted"

	case EventSessionStatus:
		var p StatusProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID = p.SessionID
		switch p.Status.Type {
		case "busy":
			ev.Status, ev.Message = adapter.StatusRunning, "working"
		case "retry":
			// A retry or two is routine (rate limits, overload). Past that the
			// provider is the problem and you should know.
			ev.Status = adapter.StatusRunning
			if p.Status.Attempt >= retriesBeforeStuck {
				ev.Status = adapter.StatusStuck
			}
			ev.Message = fmt.Sprintf("provider retrying (attempt %d): %s", p.Status.Attempt, p.Status.Message)
		case "idle":
			// session.idle fires right alongside this and carries the
			// done-vs-stuck judgment; don't publish the same fact twice.
			return adapter.SourceEvent{}, false
		default:
			ev.Status, ev.Message = adapter.StatusUnknown, "unhandled session.status: "+p.Status.Type
		}

	case EventSessionIdle:
		var p SessionProps
		_ = json.Unmarshal(env.Properties, &p)
		// Idle alone can't tell finished from stuck from "asked in plain
		// text": TurnEnded asks the daemon to read the transcript (LastTurn)
		// and judge. done is only the fallback if that read fails.
		ev.SourceID, ev.Status, ev.TurnEnded = p.SessionID, adapter.StatusDone, true
		ev.Message = "went idle"

	case EventV2ExecutionStarted:
		var p SessionProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID, ev.Status, ev.Message = p.SessionID, adapter.StatusRunning, "working"

	case EventV2ExecutionSucceeded:
		var p SessionProps
		_ = json.Unmarshal(env.Properties, &p)
		// The v2 turn-end signal (v1 used session.idle): same contract —
		// judge from the transcript, done only as the fallback.
		ev.SourceID, ev.Status, ev.TurnEnded = p.SessionID, adapter.StatusDone, true
		ev.Message = "turn finished"

	case EventV2ExecutionFailed:
		var p V2ExecutionProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID, ev.Status, ev.TurnEnded = p.SessionID, adapter.StatusFailed, true
		ev.Message = "turn failed"
		if p.Error != nil {
			ev.Message = "turn failed: " + p.Error.Message
			if p.Error.Message == "" {
				ev.Message = "turn failed: " + p.Error.Type
			}
		}

	case EventV2ExecutionInterrupt:
		var p SessionProps
		_ = json.Unmarshal(env.Properties, &p)
		// You pressed Esc: not a failure, but the work is unfinished.
		ev.SourceID, ev.Status, ev.TurnEnded = p.SessionID, adapter.StatusStuck, true
		ev.Message = "interrupted before it finished"

	case EventV2RetryScheduled:
		var p V2RetryProps
		_ = json.Unmarshal(env.Properties, &p)
		// A retry or two is routine (rate limits, overload). Past that the
		// provider is the problem and you should know.
		ev.SourceID = p.SessionID
		ev.Status = adapter.StatusRunning
		if p.Attempt >= retriesBeforeStuck {
			ev.Status = adapter.StatusStuck
		}
		ev.Message = fmt.Sprintf("provider retrying (attempt %d): %s", p.Attempt, p.Error.Message)

	case EventSessionError:
		var p ErrorProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID, ev.Status = p.SessionID, adapter.StatusFailed
		ev.Message = "session error"
		switch {
		case p.Error.Name == "MessageAbortedError":
			// You pressed Esc: not a failure, but the work is unfinished.
			ev.Status, ev.Message = adapter.StatusStuck, "interrupted before it finished"
		case p.Error.Name != "":
			ev.Message = "session error: " + p.Error.Name
		}

	case EventTodoUpdated:
		var p TodoUpdatedProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID = p.SessionID
		ev.Status, ev.Message = adapter.StatusRunning, todoProgress(p.Todos)

	case EventPermissionAsked:
		if env.API == 2 {
			var p V2PermissionAskedProps
			_ = json.Unmarshal(env.Properties, &p)
			ev.SourceID, ev.Status = p.SessionID, adapter.StatusNeedsInput
			ev.Message = "waiting on approval: " + p.Action
			if p.Message != "" {
				ev.Message = "waiting on approval: " + p.Message
			}
			ev.Ask = &adapter.Ask{
				ID:   p.ID,
				Kind: adapter.AskKindPermission,
				Questions: []adapter.AskQuestion{{
					Question: fmt.Sprintf("Allow %s %v?", p.Action, p.Resources),
					Options:  []string{"once", "always", "reject"},
				}},
			}
			break
		}
		var p PermissionAskedProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID, ev.Status = p.SessionID, adapter.StatusNeedsInput
		ev.Message = "waiting on approval"
		if p.Permission != "" {
			ev.Message = fmt.Sprintf("waiting on approval: %s %v", p.Permission, p.Patterns)
		}
		ev.Ask = &adapter.Ask{
			ID:   p.ID,
			Kind: adapter.AskKindPermission,
			Questions: []adapter.AskQuestion{{
				Question: fmt.Sprintf("Allow %s %v?", p.Permission, p.Patterns),
				Options:  []string{"once", "always", "reject"},
			}},
		}

	case EventV2FormCreated:
		var p V2FormProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID, ev.Status = p.Form.SessionID, adapter.StatusNeedsInput
		ev.Message = "asked a question"
		if len(p.Form.Fields) > 0 {
			ev.Message = "asked: " + formQuestion(p.Form.Fields[0])
		}
		ev.Ask = &adapter.Ask{ID: p.Form.ID, Kind: adapter.AskKindQuestion}
		for _, f := range p.Form.Fields {
			aq := adapter.AskQuestion{
				Question: formQuestion(f),
				Header:   f.Title,
				Key:      f.Key,
				Multiple: f.Type == "multiselect",
				FreeText: f.Custom,
			}
			for _, o := range f.Options {
				aq.Options = append(aq.Options, o.Label)
				aq.OptionValues = append(aq.OptionValues, o.Value)
			}
			ev.Ask.Questions = append(ev.Ask.Questions, aq)
		}

	case EventV2FormReplied, EventV2FormCancelled:
		var p V2FormSettledProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID, ev.Status = p.SessionID, adapter.StatusRunning
		ev.Resolves = p.ID
		ev.Message = "input received, resuming"

	case EventQuestionAsked:
		var p QuestionAskedProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID, ev.Status = p.SessionID, adapter.StatusNeedsInput
		ev.Message = "asked a question"
		if len(p.Questions) > 0 {
			ev.Message = "asked: " + p.Questions[0].Question
		}
		ev.Ask = &adapter.Ask{ID: p.ID, Kind: adapter.AskKindQuestion}
		for _, q := range p.Questions {
			aq := adapter.AskQuestion{
				Question: q.Question,
				Header:   q.Header,
				Multiple: q.Multiple,
				FreeText: q.Custom == nil || *q.Custom,
			}
			for _, o := range q.Options {
				aq.Options = append(aq.Options, o.Label)
			}
			ev.Ask.Questions = append(ev.Ask.Questions, aq)
		}

	case EventPermissionReply, EventQuestionReplied, EventQuestionRejected:
		var p ReplyProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID, ev.Status = p.SessionID, adapter.StatusRunning
		ev.Resolves = p.RequestID
		ev.Message = "input received, resuming"

	default:
		ev.Status = adapter.StatusUnknown
		ev.Capabilities = adapter.Capabilities{}
		ev.Message = fmt.Sprintf("unhandled event type: %s", env.Type)
	}

	return ev, true
}

// todoProgress describes the agent's own todo list. Todos only change
// mid-turn, so this is progress, never a verdict: whether open todos at the
// end mean "stuck" is for daemon.Judge.
func todoProgress(todos []Todo) string {
	if len(todos) == 0 {
		return "working (todo list cleared)"
	}
	finished := 0
	for _, t := range todos {
		if t.Status == "completed" || t.Status == "cancelled" {
			finished++
		}
	}
	return fmt.Sprintf("working: %d/%d todos done", finished, len(todos))
}

// formQuestion renders one v2 form field as the question the user sees.
// The description is the agent's actual question; the title is the short
// header above it.
func formQuestion(f V2FormField) string {
	if f.Description != "" {
		return f.Description
	}
	return f.Title
}
