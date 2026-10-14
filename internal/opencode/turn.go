package opencode

import (
	"context"
	"fmt"
	"strings"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

// Client reports how a session's last turn ended.
var _ adapter.TurnReader = (*Client)(nil)

// message is the subset of GET /session/{id}/message items the turn
// evidence needs. Verified against opencode 1.18.33: info.finish is e.g.
// "stop" | "tool-calls" | "length"; info.error is one of the typed errors
// below; tool parts carry state.status "completed" | "error" | ...
type message struct {
	Info struct {
		Role   string `json:"role"`
		Finish string `json:"finish"`
		Error  *struct {
			Name string `json:"name"`
			Data struct {
				Message string `json:"message"`
			} `json:"data"`
		} `json:"error"`
	} `json:"info"`
	Parts []struct {
		Type      string `json:"type"`
		Text      string `json:"text"`
		Synthetic bool   `json:"synthetic"`
		Ignored   bool   `json:"ignored"`
		Tool      string `json:"tool"`
		State     struct {
			Status string `json:"status"`
		} `json:"state"`
	} `json:"parts"`
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
