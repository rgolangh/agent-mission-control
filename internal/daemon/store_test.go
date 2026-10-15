package daemon

import (
	"fmt"
	"testing"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

func ev(src string, st adapter.Status, msg string, at time.Time) adapter.SourceEvent {
	return adapter.SourceEvent{Adapter: "opencode", SourceID: src, Status: st, Message: msg, Timestamp: at}
}

func TestApplyTracksPerSourceState(t *testing.T) {
	s := NewStore()
	t0 := time.Now()

	if !s.Apply(ev("a", adapter.StatusRunning, "working", t0)) {
		t.Fatal("first event should change state")
	}
	if s.Apply(ev("a", adapter.StatusRunning, "working", t0.Add(time.Second))) {
		t.Fatal("identical status+message should not report a change")
	}
	s.Apply(ev("a", adapter.StatusNeedsInput, "asked: x", t0.Add(2*time.Second)))
	s.Apply(ev("b", adapter.StatusDone, "idle", t0.Add(3*time.Second)))

	a, ok := s.Get("opencode:a")
	if !ok || a.Status != adapter.StatusNeedsInput {
		t.Fatalf("a = %+v, ok=%v", a, ok)
	}
	if len(a.History) != 2 {
		t.Errorf("history len = %d, want 2 (repeat was deduped)", len(a.History))
	}
	if !a.UpdatedAt.Equal(t0.Add(2 * time.Second)) {
		t.Errorf("UpdatedAt = %v", a.UpdatedAt)
	}

	list := s.List()
	if len(list) != 2 || list[0].SourceID != "b" {
		t.Errorf("list should be most-recent-first, got %+v", list)
	}
}

func TestApplyIgnoresUnknownAndUnattributed(t *testing.T) {
	s := NewStore()
	t0 := time.Now()
	s.Apply(ev("a", adapter.StatusNeedsInput, "asked: x", t0))

	// An event the adapter didn't understand must not clobber real state.
	if s.Apply(ev("a", adapter.StatusUnknown, "unhandled", t0.Add(time.Second))) {
		t.Error("unknown event should be ignored")
	}
	if s.Apply(ev("", adapter.StatusRunning, "no source", t0)) {
		t.Error("event without a source id should be ignored")
	}
	if got, _ := s.Get("opencode:a"); got.Status != adapter.StatusNeedsInput {
		t.Errorf("status clobbered: %q", got.Status)
	}
	if len(s.List()) != 1 {
		t.Errorf("unexpected workflows: %+v", s.List())
	}
}

func TestHistoryIsBounded(t *testing.T) {
	s := NewStore()
	t0 := time.Now()
	for i := 0; i < maxHistory+10; i++ {
		s.Apply(ev("a", adapter.StatusRunning, fmt.Sprintf("step %d", i), t0.Add(time.Duration(i)*time.Second)))
	}
	w, _ := s.Get("opencode:a")
	if len(w.History) != maxHistory {
		t.Fatalf("history len = %d, want %d", len(w.History), maxHistory)
	}
	if last := w.History[len(w.History)-1].Message; last != fmt.Sprintf("step %d", maxHistory+9) {
		t.Errorf("newest transition = %q", last)
	}
}

func TestWorkflowCountIsBounded(t *testing.T) {
	s := NewStore()
	t0 := time.Now().Add(-time.Hour)
	for i := 0; i < maxWorkflowsPerAdapter; i++ {
		e := ev(fmt.Sprintf("done-%d", i), adapter.StatusDone, "finished", t0.Add(time.Duration(i)*time.Second))
		if !s.Apply(e) {
			t.Fatalf("workflow %d was not admitted", i)
		}
	}
	if len(s.List()) != maxWorkflowsPerAdapter {
		t.Fatalf("workflow count = %d, want %d", len(s.List()), maxWorkflowsPerAdapter)
	}

	newDone := ev("new-done", adapter.StatusDone, "finished", time.Now())
	s.Apply(newDone)
	if len(s.List()) != maxWorkflowsPerAdapter {
		t.Fatalf("completed workflow admission grew store to %d", len(s.List()))
	}
	if _, ok := s.Get("opencode:done-0"); ok {
		t.Error("oldest completed workflow was not evicted")
	}
	if _, ok := s.Get("opencode:new-done"); !ok {
		t.Error("new completed workflow was not admitted")
	}

	active := NewStore()
	for i := 0; i < maxWorkflowsPerAdapter; i++ {
		active.Apply(ev(fmt.Sprintf("active-%d", i), adapter.StatusRunning, "working", t0.Add(time.Duration(i)*time.Second)))
	}
	if active.Apply(ev("over-limit", adapter.StatusRunning, "working", time.Now())) {
		t.Error("an active workflow was admitted after the hard limit")
	}
	if len(active.List()) != maxWorkflowsPerAdapter {
		t.Errorf("active workflow count = %d, want %d", len(active.List()), maxWorkflowsPerAdapter)
	}
}

func TestParentLinksAreBounded(t *testing.T) {
	s := NewStore()
	at := time.Now()
	for i := 0; i < maxParentLinks; i++ {
		e := ev(fmt.Sprintf("child-%d", i), adapter.StatusRunning, "working", at)
		e.Parent = fmt.Sprintf("parent-%d", i)
		s.Apply(e)
	}
	if len(s.parents) != maxParentLinks {
		t.Fatalf("parent links = %d, want %d", len(s.parents), maxParentLinks)
	}
	extra := ev("over-limit-child", adapter.StatusRunning, "working", at)
	extra.Parent = "over-limit-parent"
	if s.Apply(extra) {
		t.Error("subagent event should be rejected when the parent-link table is full")
	}
	if len(s.parents) != maxParentLinks {
		t.Errorf("parent links grew past the cap: %d", len(s.parents))
	}
}

func TestPruneIdleOnlyRemovesCompletedWorkflows(t *testing.T) {
	s := NewStore()
	old := time.Now().Add(-48 * time.Hour)
	s.Apply(adapter.SourceEvent{Adapter: "claude-code", SourceID: "done", Status: adapter.StatusDone, Message: "finished", Timestamp: old})
	s.Apply(adapter.SourceEvent{Adapter: "claude-code", SourceID: "failed", Status: adapter.StatusFailed, Message: "failed", Timestamp: old})
	s.Apply(adapter.SourceEvent{Adapter: "claude-code", SourceID: "running", Status: adapter.StatusRunning, Message: "working", Timestamp: old})
	s.Apply(adapter.SourceEvent{Adapter: "claude-code", SourceID: "waiting", Status: adapter.StatusNeedsInput, Message: "asked", Timestamp: old,
		Ask: &adapter.Ask{ID: "q", Kind: adapter.AskKindQuestion}})
	s.Apply(adapter.SourceEvent{Adapter: "opencode", SourceID: "other", Status: adapter.StatusDone, Message: "finished", Timestamp: old})

	removed := s.PruneIdle("claude-code", time.Now(), 24*time.Hour)
	if fmt.Sprint(removed) != "[claude-code:done claude-code:failed]" {
		t.Fatalf("removed = %v", removed)
	}
	for _, id := range []string{"claude-code:running", "claude-code:waiting", "opencode:other"} {
		if _, ok := s.Get(id); !ok {
			t.Errorf("pruned workflow that should remain: %s", id)
		}
	}
}

func TestMetadataOnlyEventsUpdateLabelsWithoutTouchingState(t *testing.T) {
	s := NewStore()
	t0 := time.Now()

	// Metadata for a session we have never seen must not create a workflow.
	meta := adapter.SourceEvent{Adapter: "opencode", SourceID: "a", Title: "old chat", Location: "/x", Timestamp: t0}
	if s.Apply(meta) || len(s.List()) != 0 {
		t.Fatal("metadata-only event created a ghost workflow")
	}

	s.Apply(ev("a", adapter.StatusNeedsInput, "asked: x", t0))
	meta.Title, meta.Timestamp = "Fix the login bug", t0.Add(time.Minute)
	if s.Apply(meta) {
		t.Error("metadata-only event must not report a status change")
	}
	w, _ := s.Get("opencode:a")
	if w.Title != "Fix the login bug" || w.Location != "/x" {
		t.Errorf("labels not applied: %+v", w)
	}
	if w.Status != adapter.StatusNeedsInput || len(w.History) != 1 || !w.UpdatedAt.Equal(t0) {
		t.Errorf("metadata touched state: status=%q history=%d updated=%v", w.Status, len(w.History), w.UpdatedAt)
	}

	// A later event without labels must not erase them.
	s.Apply(ev("a", adapter.StatusRunning, "working", t0.Add(2*time.Minute)))
	if w, _ = s.Get("opencode:a"); w.Title != "Fix the login bug" {
		t.Errorf("title lost after status event: %q", w.Title)
	}
}

// Agent text is stored, and so served to the CLI, the UIs and MCP, without
// terminal escapes or bidi overrides (threat model L1, N6).
func TestApplyStoresCleanText(t *testing.T) {
	s := NewStore()
	e := ev("a", adapter.StatusNeedsInput, "asked: \x1b]0;pwned\x07ok?", time.Now())
	e.Title = "fix ‮gnp.exe"
	e.Ask = &adapter.Ask{ID: "q1", Kind: adapter.AskKindQuestion, Questions: []adapter.AskQuestion{{Question: "go\x1b[2J?", Options: []string{"yes", "n\u009bo"}}}}
	s.Apply(e)
	w, _ := s.Get("opencode:a")
	if w.Message != "asked: ]0;pwnedok?" || w.Title != "fix gnp.exe" {
		t.Errorf("stored %q / %q", w.Message, w.Title)
	}
	if q := w.Pending[0].Questions[0]; q.Question != "go[2J?" || q.Options[1] != "no" {
		t.Errorf("stored ask %+v", q)
	}
}
