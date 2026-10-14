package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mission-control/internal/adapter"
	"mission-control/internal/daemon"
	"mission-control/internal/mcp"
)

// confirmingResponder plays the source: an answer is acknowledged by the
// source's own "replied" event, which is what moves the workflow's state.
type confirmingResponder struct {
	store *daemon.Store
	sent  []string
}

func (r *confirmingResponder) Answer(_ context.Context, sourceID string, ask adapter.Ask, _ [][]string) error {
	r.store.Apply(adapter.SourceEvent{Adapter: "opencode", SourceID: sourceID, Status: adapter.StatusRunning,
		Message: "answered", Resolves: ask.ID, Timestamp: time.Now(), Capabilities: adapter.Capabilities{CanReceiveInput: true}})
	return nil
}

func (r *confirmingResponder) SendText(_ context.Context, _, text string) error {
	r.sent = append(r.sent, text)
	return nil
}

func call(t *testing.T, tools []mcp.Tool, name, args string) (string, error) {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool.Handler(context.Background(), json.RawMessage(args))
		}
	}
	t.Fatalf("no tool %q", name)
	return "", nil
}

func TestMCPToolsAgainstDaemon(t *testing.T) {
	store := daemon.NewStore()
	r := &confirmingResponder{store: store}
	now := time.Now()
	caps := adapter.Capabilities{CanReceiveInput: true}
	store.Apply(adapter.SourceEvent{Adapter: "opencode", SourceID: "ses_waiting1", Status: adapter.StatusNeedsInput,
		Message: "asked", Title: "refactor", Timestamp: now, Capabilities: caps,
		Ask: &adapter.Ask{ID: "que_1", Kind: adapter.AskKindQuestion, Questions: []adapter.AskQuestion{{Question: "Tabs or spaces?", Options: []string{"Tabs", "Spaces"}}}}})
	store.Apply(adapter.SourceEvent{Adapter: "opencode", SourceID: "ses_finished", Status: adapter.StatusDone,
		Message: "idle", Timestamp: now.Add(-time.Minute), Capabilities: caps})
	store.Bind("opencode:ses_waiting1", r, "test")
	store.Bind("opencode:ses_finished", r, "test")

	srv := httptest.NewServer(daemon.Handler(store)) // 127.0.0.1, so the loopback guard passes
	defer srv.Close()
	tools := mcpTools(srv.URL)

	out, err := call(t, tools, "list_workflows", `{"status":"needs_input"}`)
	if err != nil || !strings.Contains(out, "Tabs or spaces?") || strings.Contains(out, "ses_finished") {
		t.Fatalf("list needs_input: %v\n%s", err, out)
	}
	if _, err := call(t, tools, "list_workflows", `{"status":"bogus"}`); err != nil {
		t.Errorf("an unmatched status filter is just an empty list: %v", err)
	}

	if _, err := call(t, tools, "send_to_workflow", `{"id":"aiting1","text":"go on"}`); err == nil {
		t.Error("send must be refused while an ask is pending")
	}
	if _, err := call(t, tools, "answer_workflow", `{"id":"aiting1","choice":"Neither"}`); err == nil {
		t.Error("an answer outside the options must be rejected")
	}

	out, err = call(t, tools, "answer_workflow", `{"id":"aiting1","choice":"2"}`)
	if err != nil || !strings.Contains(out, `"confirmed": true`) || !strings.Contains(out, `"Spaces"`) {
		t.Fatalf("answer: %v\n%s", err, out)
	}

	if _, err := call(t, tools, "send_to_workflow", `{"id":"finished","text":"also add tests"}`); err != nil || len(r.sent) != 1 {
		t.Fatalf("send: %v %v", err, r.sent)
	}

	out, err = call(t, tools, "get_workflow", `{"id":"opencode:ses_waiting1"}`)
	if err != nil || !strings.Contains(out, `"history"`) || strings.Contains(out, `"pending"`) {
		t.Fatalf("get after answer: %v\n%s", err, out)
	}
	if _, err := call(t, tools, "get_workflow", `{"id":"zzzzzzzz"}`); err == nil {
		t.Error("unknown id must error")
	}
}
