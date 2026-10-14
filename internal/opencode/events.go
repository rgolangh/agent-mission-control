package opencode

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

// Envelope is the wire shape of every frame OpenCode sends on GET /event:
// { id, type, properties }. The extra top-level `id` is ignored.
//
// Everything below was verified against a live `opencode serve` (v1.18.30):
// the type strings and property shapes come from its /doc OpenAPI spec, and
// the started -> question -> answer -> idle lifecycle was observed on the
// real /event stream. These are still internal event names, not a published
// contract — they change between releases (note the parallel `*.v2.*`
// families in the spec), so re-verify after upgrading OpenCode.
type Envelope struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
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
)

// retriesBeforeStuck is the retry attempt at which a session counts as stuck
// on its provider rather than briefly rate limited.
const retriesBeforeStuck = 3

// noisy lists high-volume frames that carry no workflow-level state. They are
// dropped by Normalize; anything NOT listed here and not handled below is
// still surfaced as StatusUnknown so new event types stay visible.
var noisy = map[string]bool{
	"server.connected":     true,
	"server.heartbeat":     true,
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
		var p SessionInfoProps
		_ = json.Unmarshal(env.Properties, &p)
		ev.SourceID, ev.Status, ev.Message = p.SessionID, adapter.StatusRunning, "session started"
		ev.Title, ev.Location, ev.Parent = p.Info.Title, p.Info.Directory, p.Info.ParentID

	case EventSessionUpdated:
		// Metadata only (retitle, cost, summary…): no Status, so the daemon
		// refreshes the label without touching state or history.
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
