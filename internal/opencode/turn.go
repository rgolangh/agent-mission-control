package opencode

import (
	"context"
	"fmt"
	"strings"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

// Client reports how a session's last turn ended.
var _ adapter.TurnReader = (*Client)(nil)

// messageInfo is the header of a v1 transcript item
// (GET /session/{id}/message). Verified against opencode 1.18.33:
// info.finish is e.g. "stop" | "tool-calls" | "length"; info.error is one
// of the typed errors below.
type messageInfo struct {
	Role   string        `json:"role"`
	Finish string        `json:"finish"`
	Error  *messageError `json:"error"`
}

type messageError struct {
	Name string `json:"name"`
	Data struct {
		Message string `json:"message"`
	} `json:"data"`
}

type messagePart struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Synthetic bool   `json:"synthetic"`
	Ignored   bool   `json:"ignored"`
	Tool      string `json:"tool"`
	State     struct {
		Status string `json:"status"`
	} `json:"state"`
}

// message is the subset of the v1 transcript items the turn evidence needs.
// Tool parts carry state.status "completed" | "error" | ...
type message struct {
	Info  messageInfo   `json:"info"`
	Parts []messagePart `json:"parts"`
}

// v2Message is the subset of the v2 message list items the turn evidence
// needs (GET /api/session/{id}/message, newest first). Verified against
// opencode 2.2.5: assistant items carry finish ("stop" | "length" |
// "tool-calls" | "content-filter" | "error" | "unknown") and an error
// {type, message}; their content parts are text or tool, the tool state's
// status being "completed" | "error" | ... Unlike v1 there is no todo API,
// so the v2 turn evidence reports no todos.
type v2Message struct {
	Type   string `json:"type"` // "user" | "assistant" | "idle" | ...
	Text   string `json:"text"`
	Finish string `json:"finish"`
	Error  *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	Content []struct {
		Type  string `json:"type"` // "text" | "tool" | "reasoning"
		Text  string `json:"text"`
		Name  string `json:"name"`
		State struct {
			Status string `json:"status"`
		} `json:"state"`
	} `json:"content"`
}

// errorKinds maps OpenCode's message error names to TurnEnd.Error kinds.
var errorKinds = map[string]string{
	"MessageAbortedError":      adapter.TurnErrorAborted,
	"MessageOutputLengthError": adapter.TurnErrorOutputLength,
	"ContextOverflowError":     adapter.TurnErrorContextOverflow,
	"ContentFilterError":       adapter.TurnErrorContentFilter,
	"ProviderAuthError":        adapter.TurnErrorAuth,
	"APIError":                 adapter.TurnErrorAPI,
}

// turnWindow is how many trailing messages are read. A turn is one user
// message followed by one assistant message per step; the evidence lives in
// the last few.
const turnWindow = 8

// LastTurn reads the end of a session's transcript and its todo list.
func (c *Client) LastTurn(ctx context.Context, sessionID string) (adapter.TurnEnd, error) {
	if c.apiVersion(ctx) == 2 {
		return c.lastTurnV2(ctx, sessionID)
	}
	var msgs []message
	if err := c.get(ctx, fmt.Sprintf("/session/%s/message?limit=%d", sessionID, turnWindow), &msgs); err != nil {
		return adapter.TurnEnd{}, err
	}
	t := turnFromMessages(msgs)

	var todos []Todo
	if err := c.get(ctx, fmt.Sprintf("/session/%s/todo", sessionID), &todos); err == nil {
		for _, td := range todos {
			t.TodosTotal++
			if td.Status == "pending" || td.Status == "in_progress" {
				t.TodosOpen++
			}
		}
	}
	return t, nil
}

// lastTurnV2 reads the newest page of the transcript (it comes newest
// first), reverses it to chronological order, and reuses the same
// turnFromMessages evidence extraction as v1.
func (c *Client) lastTurnV2(ctx context.Context, sessionID string) (adapter.TurnEnd, error) {
	var resp struct {
		Data []v2Message `json:"data"`
	}
	if err := c.get(ctx, fmt.Sprintf("/api/session/%s/message?limit=%d", sessionID, turnWindow), &resp); err != nil {
		return adapter.TurnEnd{}, err
	}
	msgs := make([]message, 0, len(resp.Data))
	for i := len(resp.Data) - 1; i >= 0; i-- {
		m := resp.Data[i]
		switch m.Type {
		case "user":
			msgs = append(msgs, message{Info: messageInfo{Role: "user"}})
		case "assistant":
			am := message{Info: messageInfo{Role: "assistant", Finish: m.Finish}}
			if m.Error != nil {
				am.Info.Error = &messageError{Name: m.Error.Type}
				am.Info.Error.Data.Message = m.Error.Message
			}
			for _, part := range m.Content {
				am.Parts = append(am.Parts, messagePart{
					Type: part.Type, Text: part.Text, Tool: part.Name,
					State: struct {
						Status string `json:"status"`
					}{Status: part.State.Status},
				})
			}
			msgs = append(msgs, am)
		default:
			// idle markers, system notes, compaction summaries, ... — no turn evidence
		}
	}
	return turnFromMessages(msgs), nil
}

// turnFromMessages extracts the evidence from messages in chronological
// order: the assistant messages after the last user message are this turn.
func turnFromMessages(msgs []message) adapter.TurnEnd {
	var t adapter.TurnEnd
	start := 0
	for i, m := range msgs {
		if m.Info.Role == "user" {
			start = i + 1
		}
	}
	turn := msgs[start:]
	if len(turn) == 0 {
		return t // idle right after the user spoke: no reply
	}
	t.Replied = true

	last := turn[len(turn)-1]
	t.Finish = last.Info.Finish
	if e := last.Info.Error; e != nil {
		t.Error = errorKinds[e.Name]
		if t.Error == "" {
			t.Error = adapter.TurnErrorOther
		}
		t.ErrorMessage = e.Data.Message
		if t.ErrorMessage == "" {
			t.ErrorMessage = e.Name
		}
	}

	// The final words: the text of the last message that has any (the last
	// step may be a bare tool call). A failed tool call with no text after it
	// in the final message is what stopped the agent.
	for i := len(turn) - 1; i >= 0 && t.Text == ""; i-- {
		var b strings.Builder
		for _, p := range turn[i].Parts {
			if p.Type == "text" && !p.Synthetic && !p.Ignored {
				b.WriteString(p.Text)
				b.WriteString("\n")
			}
		}
		t.Text = strings.TrimSpace(b.String())
	}
	for i := len(last.Parts) - 1; i >= 0; i-- {
		p := last.Parts[i]
		if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
			break
		}
		if p.Type == "tool" {
			if p.State.Status == "error" {
				t.LastToolFailed = p.Tool
			}
			break
		}
	}
	return t
}
