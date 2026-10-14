package opencode

import (
	"encoding/json"
	"testing"

	"mission-control/internal/adapter"
)

// Shapes trimmed from GET /session/{id}/message on opencode 1.18.33.
func msgs(t *testing.T, js string) []message {
	t.Helper()
	var m []message
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestTurnFromMessages(t *testing.T) {
	user := `{"info":{"role":"user"},"parts":[{"type":"text","text":"do it"}]}`

	got := turnFromMessages(msgs(t, `[`+user+`,
		{"info":{"role":"assistant","finish":"tool-calls"},"parts":[{"type":"step-start"},{"type":"tool","tool":"bash","state":{"status":"completed"}},{"type":"step-finish","reason":"tool-calls"}]},
		{"info":{"role":"assistant","finish":"stop"},"parts":[{"type":"step-start"},{"type":"reasoning","text":"hmm"},{"type":"text","text":"Which port?"},{"type":"step-finish","reason":"stop"}]}]`))
	if !got.Replied || got.Finish != "stop" || got.Text != "Which port?" || got.LastToolFailed != "" {
		t.Errorf("normal turn: %+v", got)
	}

	got = turnFromMessages(msgs(t, `[`+user+`,
		{"info":{"role":"assistant","finish":"stop"},"parts":[{"type":"text","text":"Running tests now."}]},
		{"info":{"role":"assistant","finish":"tool-calls"},"parts":[{"type":"tool","tool":"bash","state":{"status":"error"}}]}]`))
	if got.LastToolFailed != "bash" || got.Text != "Running tests now." || got.Finish != "tool-calls" {
		t.Errorf("failed tool last: %+v", got)
	}

	got = turnFromMessages(msgs(t, `[`+user+`,
		{"info":{"role":"assistant","error":{"name":"MessageAbortedError","data":{"message":"The operation was aborted."}}},"parts":[{"type":"text","text":"Let me"}]}]`))
	if got.Error != adapter.TurnErrorAborted || got.ErrorMessage != "The operation was aborted." {
		t.Errorf("aborted: %+v", got)
	}

	got = turnFromMessages(msgs(t, `[{"info":{"role":"assistant","error":{"name":"SomethingNew"}},"parts":[]}]`))
	if got.Error != adapter.TurnErrorOther || got.ErrorMessage != "SomethingNew" || !got.Replied {
		t.Errorf("unknown error, user message outside the window: %+v", got)
	}

	if got = turnFromMessages(msgs(t, `[`+user+`]`)); got.Replied {
		t.Errorf("no reply: %+v", got)
	}

	got = turnFromMessages(msgs(t, `[`+user+`,{"info":{"role":"assistant","finish":"stop"},"parts":[{"type":"text","text":"x","synthetic":true},{"type":"text","text":"Done."}]}]`))
	if got.Text != "Done." {
		t.Errorf("synthetic text must be skipped: %+v", got)
	}
}
