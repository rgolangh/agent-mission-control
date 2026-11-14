package opencode

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

// Frames below are trimmed copies of real /event output from opencode 1.18.30.
func TestNormalizeSessionLabels(t *testing.T) {
	created := `{"type":"session.created","properties":{"sessionID":"ses_1","info":{"id":"ses_1","title":"New session - 2026","directory":"/home/user/proj"}}}`
	updated := `{"type":"session.updated","properties":{"sessionID":"ses_1","info":{"id":"ses_1","title":"Fix login bug","directory":"/home/user/proj"}}}`

	var env Envelope
	json.Unmarshal([]byte(created), &env)
	ev, ok := Normalize(env)
	if !ok || ev.Status != adapter.StatusRunning || ev.Title != "New session - 2026" || ev.Location != "/home/user/proj" {
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

// --- v2 (the shared service's /api/event stream) ---

// Frames below are trimmed copies of real /api/event output from opencode
// 2.2.5. The v2 wire shape carries the payload under `data`.
func v2Envelope(t *testing.T, frame string) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal([]byte(frame), &env); err != nil {
		t.Fatal(err)
	}
	env.API = 2
	return env
}

func TestEnvelopeDecodesV2Frames(t *testing.T) {
	env := v2Envelope(t, `{"id":"evt_1","created":1,"type":"session.idle","location":{"directory":"/tmp/x"},"data":{"sessionID":"ses_1"}}`)
	if env.Type != "session.idle" || string(env.Properties) != `{"sessionID":"ses_1"}` {
		t.Errorf("v2 frame decoded to %+v", env)
	}
}

func TestNormalizeV2Lifecycle(t *testing.T) {
	frames := []struct {
		name        string
		json        string
		wantOK      bool
		wantStatus  adapter.Status
		wantTurnEnd bool
		wantMsg     string
	}{
		{"created", `{"type":"session.created","data":{"sessionID":"ses_1","slug":"hidden-forest","location":{"directory":"/tmp/x"}}}`, true, adapter.StatusRunning, false, "session started"},
		{"execution started", `{"type":"session.execution.started","data":{"sessionID":"ses_1"}}`, true, adapter.StatusRunning, false, "working"},
		{"step noise dropped", `{"type":"session.step.started","data":{"sessionID":"ses_1"}}`, false, "", false, ""},
		{"usage noise dropped", `{"type":"session.usage.updated","data":{"sessionID":"ses_1","cost":1}}`, false, "", false, ""},
		{"retry", `{"type":"session.retry.scheduled","data":{"sessionID":"ses_1","attempt":2,"error":{"message":"overloaded"}}}`, true, adapter.StatusRunning, false, "provider retrying (attempt 2): overloaded"},
		{"retry stuck", `{"type":"session.retry.scheduled","data":{"sessionID":"ses_1","attempt":3,"error":{"message":"overloaded"}}}`, true, adapter.StatusStuck, false, "provider retrying (attempt 3): overloaded"},
		{"succeeded ends the turn", `{"type":"session.execution.succeeded","data":{"sessionID":"ses_1"}}`, true, adapter.StatusDone, true, "turn finished"},
		{"failed ends the turn", `{"type":"session.execution.failed","data":{"sessionID":"ses_1","error":{"type":"APIError","message":"boom"}}}`, true, adapter.StatusFailed, true, "turn failed: boom"},
		{"interrupted ends the turn", `{"type":"session.execution.interrupted","data":{"sessionID":"ses_1"}}`, true, adapter.StatusStuck, true, "interrupted before it finished"},
	}
	for _, f := range frames {
		t.Run(f.name, func(t *testing.T) {
			ev, ok := Normalize(v2Envelope(t, f.json))
			if ok != f.wantOK {
				t.Fatalf("ok = %v, want %v", ok, f.wantOK)
			}
			if !ok {
				return
			}
			if ev.Status != f.wantStatus {
				t.Errorf("status = %q, want %q", ev.Status, f.wantStatus)
			}
			if ev.TurnEnded != f.wantTurnEnd {
				t.Errorf("turn ended = %v, want %v", ev.TurnEnded, f.wantTurnEnd)
			}
			if f.wantMsg != "" && ev.Message != f.wantMsg {
				t.Errorf("message = %q, want %q", ev.Message, f.wantMsg)
			}
		})
	}
}

func TestNormalizeV2SessionLabels(t *testing.T) {
	ev, ok := Normalize(v2Envelope(t, `{"type":"session.created","data":{"sessionID":"ses_1","slug":"hidden-forest","parentID":"ses_parent","location":{"directory":"/tmp/x"}}}`))
	if !ok || ev.Title != "hidden-forest" || ev.Location != "/tmp/x" || ev.Parent != "ses_parent" {
		t.Errorf("v2 session.created = ok:%v %+v", ok, ev)
	}

	ev, ok = Normalize(v2Envelope(t, `{"type":"session.renamed","data":{"sessionID":"ses_1","title":"Fix login bug"}}`))
	if !ok || ev.Status != "" || ev.Title != "Fix login bug" {
		t.Errorf("v2 session.renamed should be metadata-only, got ok:%v %+v", ok, ev)
	}
}

func TestNormalizeV2Permission(t *testing.T) {
	ev, ok := Normalize(v2Envelope(t, `{"type":"permission.asked","data":{"id":"per_1","sessionID":"ses_1","action":"bash","resources":["rm -rf /"]}}`))
	if !ok || ev.Status != adapter.StatusNeedsInput || ev.Ask == nil || ev.Ask.ID != "per_1" {
		t.Fatalf("v2 permission.asked = ok:%v %+v", ok, ev)
	}
	if q := ev.Ask.Questions[0]; q.Question != "Allow bash [rm -rf /]?" || len(q.Options) != 3 || q.Options[0] != "once" {
		t.Errorf("permission question = %+v", q)
	}

	ev, ok = Normalize(v2Envelope(t, `{"type":"permission.replied","data":{"sessionID":"ses_1","requestID":"per_1","reply":"once"}}`))
	if !ok || ev.Resolves != "per_1" || ev.Status != adapter.StatusRunning {
		t.Errorf("v2 permission.replied = ok:%v %+v", ok, ev)
	}
}

// Trimmed from a live question-tool run on opencode 2.2.5: the tool creates
// a form with one string field with options and custom answers allowed.
func TestNormalizeV2Form(t *testing.T) {
	ev, ok := Normalize(v2Envelope(t, `{"type":"form.created","data":{"form":{"id":"frm_1","sessionID":"ses_1","title":"Questions","fields":[{"key":"q0","title":"Color choice","description":"Which color?","type":"string","options":[{"value":"red","label":"red"},{"value":"blue","label":"blue"}],"custom":true}]}}}`))
	if !ok || ev.Status != adapter.StatusNeedsInput || ev.Ask == nil || ev.Ask.ID != "frm_1" || ev.Ask.Kind != adapter.AskKindQuestion {
		t.Fatalf("form.created = ok:%v %+v", ok, ev)
	}
	q := ev.Ask.Questions[0]
	if q.Key != "q0" || q.Header != "Color choice" || q.Question != "Which color?" || !q.FreeText {
		t.Errorf("form question = %+v", q)
	}
	if len(q.Options) != 2 || len(q.OptionValues) != 2 || q.OptionValues[0] != "red" {
		t.Errorf("form options = %v values = %v", q.Options, q.OptionValues)
	}

	ev, ok = Normalize(v2Envelope(t, `{"type":"form.replied","data":{"id":"frm_1","sessionID":"ses_1","answer":{"q0":"red"}}}`))
	if !ok || ev.Resolves != "frm_1" || ev.Status != adapter.StatusRunning {
		t.Errorf("form.replied = ok:%v %+v", ok, ev)
	}

	ev, ok = Normalize(v2Envelope(t, `{"type":"form.cancelled","data":{"id":"frm_1","sessionID":"ses_1"}}`))
	if !ok || ev.Resolves != "frm_1" || ev.Status != adapter.StatusRunning {
		t.Errorf("form.cancelled = ok:%v %+v", ok, ev)
	}
}

func TestReplyFormMapsLabelsToValues(t *testing.T) {
	c, got := capture(t)
	c.API = 2 // skip version detection against the v1-shaped fake
	ask := adapter.Ask{
		ID: "frm_1", Kind: adapter.AskKindQuestion,
		Questions: []adapter.AskQuestion{
			{Key: "q0", Options: []string{"Red", "Blue"}, OptionValues: []string{"red", "blue"}},
			{Key: "q1", Multiple: true, Options: []string{"A", "B"}, OptionValues: []string{"a", "b"}},
			{Key: "q2", FreeText: true},
		},
	}
	if err := c.Answer(context.Background(), "ses_1", ask, [][]string{{"Blue"}, {"A", "B"}, {"typed"}}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 { // just the reply: the API version is pinned
		t.Fatalf("requests = %v", *got)
	}
	r := (*got)[0]
	if r.method != "POST" || r.path != "/api/session/ses_1/form/frm_1/reply" {
		t.Errorf("request = %s %s", r.method, r.path)
	}
	var body struct {
		Answer map[string]any `json:"answer"`
	}
	if err := json.Unmarshal([]byte(r.body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Answer["q0"] != "blue" {
		t.Errorf("q0 = %v, want blue (mapped from the label)", body.Answer["q0"])
	}
	if arr, ok := body.Answer["q1"].([]any); !ok || len(arr) != 2 || arr[0] != "a" {
		t.Errorf("q1 = %v, want [a b]", body.Answer["q1"])
	}
	if body.Answer["q2"] != "typed" {
		t.Errorf("q2 = %v, want the typed answer", body.Answer["q2"])
	}
}
