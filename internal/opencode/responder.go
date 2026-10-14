package opencode

import (
	"context"
	"fmt"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

// Client is OpenCode's adapter.Responder.
var _ adapter.Responder = (*Client)(nil)

// Answer delivers answers to a pending ask. A question takes one list of
// chosen labels per question; a permission takes exactly one of
// "once" | "always" | "reject".
func (c *Client) Answer(ctx context.Context, _ string, ask adapter.Ask, answers [][]string) error {
	switch ask.Kind {
	case adapter.AskKindQuestion:
		return c.ReplyQuestion(ctx, ask.ID, answers)
	case adapter.AskKindPermission:
		if len(answers) != 1 || len(answers[0]) != 1 {
			return fmt.Errorf("a permission takes exactly one reply, got %v", answers)
		}
		return c.ResolvePermission(ctx, ask.ID, answers[0][0])
	default:
		return fmt.Errorf("unsupported ask kind %q", ask.Kind)
	}
}

// SendText sends free text into a session, as if typed into its prompt.
func (c *Client) SendText(ctx context.Context, sourceID, text string) error {
	return c.PromptAsync(ctx, sourceID, text)
}
