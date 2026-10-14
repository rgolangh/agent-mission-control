package opencode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

type captured struct{ method, path, body string }

func capture(t *testing.T) (*Client, *[]captured) {
	t.Helper()
	var got []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, captured{r.Method, r.URL.Path, string(b)})
		w.Write([]byte(`true`))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL), &got
}

func TestResponderWireRequests(t *testing.T) {
	ctx := context.Background()
	c, got := capture(t)

	q := adapter.Ask{ID: "que_1", Kind: adapter.AskKindQuestion}
	if err := c.Answer(ctx, "ses_1", q, [][]string{{"Spaces"}}); err != nil {
		t.Fatal(err)
	}
	p := adapter.Ask{ID: "per_1", Kind: adapter.AskKindPermission}
	if err := c.Answer(ctx, "ses_1", p, [][]string{{"once"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.SendText(ctx, "ses_1", "add tests"); err != nil {
		t.Fatal(err)
	}

	want := []captured{
		{"POST", "/question/que_1/reply", `{"answers":[["Spaces"]]}`},
		{"POST", "/permission/per_1/reply", `{"reply":"once"}`},
		{"POST", "/session/ses_1/prompt_async", `{"parts":[{"text":"add tests","type":"text"}]}`},
	}
	if len(*got) != len(want) {
		t.Fatalf("requests = %v", *got)
	}
	for i, w := range want {
		g := (*got)[i]
		if g.method != w.method || g.path != w.path {
			t.Errorf("request %d = %s %s, want %s %s", i, g.method, g.path, w.method, w.path)
		}
		var a, b any
		json.Unmarshal([]byte(g.body), &a)
		json.Unmarshal([]byte(w.body), &b)
		if ja, jb := mustJSON(a), mustJSON(b); ja != jb {
			t.Errorf("request %d body = %s, want %s", i, ja, jb)
		}
	}

	// A permission takes exactly one reply; never guess.
	if err := c.Answer(ctx, "ses_1", p, [][]string{{"once", "always"}}); err == nil {
		t.Error("permission accepted two replies")
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestNormalizeAsksCarryAnswerableDetail(t *testing.T) {
	var env Envelope
	json.Unmarshal([]byte(`{"type":"question.asked","properties":{"id":"que_1","sessionID":"ses_1","questions":[
		{"question":"Tabs or spaces?","header":"Indent","options":[{"label":"Tabs","description":"t"},{"label":"Spaces","description":"s"}]},
		{"question":"Which files?","header":"Files","multiple":true,"custom":false,"options":[{"label":"a","description":""}]}]}}`), &env)
	ev, ok := Normalize(env)
	if !ok || ev.Ask == nil || ev.Ask.ID != "que_1" || ev.Ask.Kind != adapter.AskKindQuestion || len(ev.Ask.Questions) != 2 {
		t.Fatalf("question ask = %+v", ev.Ask)
	}
	q1, q2 := ev.Ask.Questions[0], ev.Ask.Questions[1]
	if len(q1.Options) != 2 || q1.Options[1] != "Spaces" || !q1.FreeText || q1.Multiple {
		t.Errorf("q1 (custom absent => free text allowed) = %+v", q1)
	}
	if !q2.Multiple || q2.FreeText {
		t.Errorf("q2 (multiple, custom:false) = %+v", q2)
	}

	json.Unmarshal([]byte(`{"type":"permission.asked","properties":{"id":"per_1","sessionID":"ses_1","permission":"bash","patterns":["rm *"],"always":[],"metadata":{}}}`), &env)
	ev, _ = Normalize(env)
	if ev.Ask == nil || ev.Ask.ID != "per_1" || ev.Ask.Kind != adapter.AskKindPermission ||
		len(ev.Ask.Questions) != 1 || ev.Ask.Questions[0].FreeText || len(ev.Ask.Questions[0].Options) != 3 {
		t.Errorf("permission ask = %+v", ev.Ask)
	}

	for _, typ := range []string{"question.replied", "question.rejected", "permission.replied"} {
		json.Unmarshal([]byte(`{"type":"`+typ+`","properties":{"sessionID":"ses_1","requestID":"req_9"}}`), &env)
		if ev, _ = Normalize(env); ev.Resolves != "req_9" || ev.Status != adapter.StatusRunning {
			t.Errorf("%s: resolves=%q status=%q", typ, ev.Resolves, ev.Status)
		}
	}
}
