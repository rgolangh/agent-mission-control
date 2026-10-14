package opencode

import (
	"encoding/json"
	"testing"

	"mission-control/internal/adapter"
)

// Frames below are trimmed copies of real /event output from opencode 1.18.30.
func TestNormalizeSessionLabels(t *testing.T) {
	created := `{"type":"session.created","properties":{"sessionID":"ses_1","info":{"id":"ses_1","title":"New session - 2026","directory":"/home/roy/proj"}}}`
	updated := `{"type":"session.updated","properties":{"sessionID":"ses_1","info":{"id":"ses_1","title":"Fix login bug","directory":"/home/roy/proj"}}}`

	var env Envelope
	json.Unmarshal([]byte(created), &env)
	ev, ok := Normalize(env)
	if !ok || ev.Status != adapter.StatusRunning || ev.Title != "New session - 2026" || ev.Location != "/home/roy/proj" {
		t.Errorf("session.created = ok:%v %+v", ok, ev)
	}

	json.Unmarshal([]byte(updated), &env)
	ev, ok = Normalize(env)
	if !ok || ev.Status != "" || ev.Title != "Fix login bug" || ev.SourceID != "ses_1" {
		t.Errorf("session.updated should be metadata-only with the new title, got ok:%v %+v", ok, ev)
	}
}

func TestNormalizeQuestionLifecycle(t *testing.T) {
	frames := []struct {
		name       string
		json       string
		wantOK     bool
		wantStatus adapter.Status
		wantSource string
		wantMsg    string
	}{
		{"created", `{"id":"evt_1","type":"session.created","properties":{"sessionID":"ses_1","info":{"id":"ses_1"}}}`, true, adapter.StatusRunning, "ses_1", "session started"},
		{"busy", `{"type":"session.status","properties":{"sessionID":"ses_1","status":{"type":"busy"}}}`, true, adapter.StatusRunning, "ses_1", "working"},
		{"question", `{"type":"question.asked","properties":{"id":"que_1","sessionID":"ses_1","questions":[{"question":"Tabs or spaces?","header":"Indent","options":[{"label":"Tabs"},{"label":"Spaces"}]}]}}`, true, adapter.StatusNeedsInput, "ses_1", "asked: Tabs or spaces?"},
		{"replied", `{"type":"question.replied","properties":{"sessionID":"ses_1","requestID":"que_1","answers":[["Spaces"]]}}`, true, adapter.StatusRunning, "ses_1", "input received, resuming"},
		{"status idle is deduped", `{"type":"session.status","properties":{"sessionID":"ses_1","status":{"type":"idle"}}}`, false, "", "", ""},
		{"idle", `{"type":"session.idle","properties":{"sessionID":"ses_1"}}`, true, adapter.StatusDone, "ses_1", ""},
		{"permission asked", `{"type":"permission.asked","properties":{"id":"per_1","sessionID":"ses_1","permission":"bash","patterns":["ls *"],"always":[],"metadata":{}}}`, true, adapter.StatusNeedsInput, "ses_1", "waiting on approval: bash [ls *]"},
		{"permission replied is not needs_input", `{"type":"permission.replied","properties":{"sessionID":"ses_1","requestID":"per_1","reply":"once"}}`, true, adapter.StatusRunning, "ses_1", ""},
		{"error", `{"type":"session.error","properties":{"sessionID":"ses_1","error":{"name":"APIError","data":{}}}}`, true, adapter.StatusFailed, "ses_1", "session error: APIError"},
		{"noise dropped", `{"type":"message.part.delta","properties":{"sessionID":"ses_1"}}`, false, "", "", ""},
		{"heartbeat dropped", `{"type":"server.heartbeat","properties":{}}`, false, "", "", ""},
		{"new event type stays visible", `{"type":"something.new","properties":{}}`, true, adapter.StatusUnknown, "", "unhandled event type: something.new"},
	}
	for _, f := range frames {
		t.Run(f.name, func(t *testing.T) {
			var env Envelope
			if err := json.Unmarshal([]byte(f.json), &env); err != nil {
				t.Fatal(err)
			}
			ev, ok := Normalize(env)
			if ok != f.wantOK {
				t.Fatalf("ok = %v, want %v", ok, f.wantOK)
			}
			if !ok {
				return
			}
			if ev.Status != f.wantStatus {
				t.Errorf("status = %q, want %q", ev.Status, f.wantStatus)
			}
			if ev.SourceID != f.wantSource {
				t.Errorf("source = %q, want %q", ev.SourceID, f.wantSource)
			}
			if f.wantMsg != "" && ev.Message != f.wantMsg {
				t.Errorf("message = %q, want %q", ev.Message, f.wantMsg)
			}
		})
	}
}

func TestNormalizeSessionDeleted(t *testing.T) {
	var env Envelope
	json.Unmarshal([]byte(`{"type":"session.deleted","properties":{"sessionID":"ses_1","info":{"id":"ses_1"}}}`), &env)
	ev, ok := Normalize(env)
	if !ok || !ev.Gone || ev.SourceID != "ses_1" {
		t.Errorf("session.deleted = ok:%v %+v", ok, ev)
	}
}

// Trimmed from a live subagent (task tool) run on opencode 1.18.33.
func TestNormalizeSubagentParent(t *testing.T) {
	frame := `{"type":"session.created","properties":{"sessionID":"ses_kid","info":{"id":"ses_kid","parentID":"ses_parent","title":"Ask color (@general subagent)","directory":"/tmp/x"}}}`
	var env Envelope
	json.Unmarshal([]byte(frame), &env)
	ev, ok := Normalize(env)
	if !ok || ev.Parent != "ses_parent" || ev.SourceID != "ses_kid" {
		t.Errorf("subagent session.created = ok:%v %+v", ok, ev)
	}
}
