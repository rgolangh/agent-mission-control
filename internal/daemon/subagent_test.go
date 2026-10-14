package daemon

import (
	"context"
	"testing"
	"time"

	"mission-control/internal/adapter"
)

func child(src, parent string, st adapter.Status, msg string, at time.Time) adapter.SourceEvent {
	e := ev(src, st, msg, at)
	e.Parent = parent
	return e
}

func TestSubagentFoldsIntoParent(t *testing.T) {
	s, r := NewStore(), &fakeResponder{}
	t0 := time.Now()
	s.Apply(ev("parent1", adapter.StatusRunning, "working", t0))
	s.Bind("opencode:parent1", r, "o")

	// The subagent starts, works and its asks surface on the parent.
	s.Apply(child("kid1", "parent1", adapter.StatusRunning, "session started", t0.Add(time.Second)))
	s.Apply(ev("kid1", adapter.StatusRunning, "working", t0.Add(2*time.Second))) // later frames carry no Parent
	if _, ok := s.Get("opencode:kid1"); ok {
		t.Fatal("a subagent must not become a workflow of its own")
	}
	if id := s.WorkflowFor(ev("kid1", adapter.StatusRunning, "", t0)); id != "opencode:parent1" {
		t.Fatalf("WorkflowFor(kid) = %q", id)
	}
	if !s.Apply(askEvent("kid1", question("q1", false, false, "Red", "Blue"), t0.Add(3*time.Second))) {
		t.Fatal("subagent ask should change the parent's state")
	}
	p, _ := s.Get("opencode:parent1")
	if p.Status != adapter.StatusNeedsInput || len(p.Pending) != 1 || p.Message != "subagent asked: Tabs or spaces?" {
		t.Fatalf("parent = %+v", p)
	}

	// The parent's binding delivers the answer (asks route by ID).
	if _, err := s.Answer(context.Background(), "parent1", AnswerRequest{Choice: "Blue"}); err != nil || len(r.answers) != 1 {
		t.Fatalf("answer via parent: %v %v", err, r.answers)
	}

	// The parent's own busy frames must not hide the subagent's ask.
	s.Apply(ev("parent1", adapter.StatusRunning, "working", t0.Add(4*time.Second)))
	if p, _ := s.Get("opencode:parent1"); p.Status != adapter.StatusNeedsInput {
		t.Fatalf("busy parent overwrote the subagent's ask: %+v", p)
	}

	s.Apply(resolveEvent("kid1", "q1", t0.Add(5*time.Second)))
	if p, _ := s.Get("opencode:parent1"); p.Status != adapter.StatusRunning || len(p.Pending) != 0 {
		t.Fatalf("after resolve: %+v", p)
	}

	// The subagent finishing is not the parent finishing.
	if s.Apply(ev("kid1", adapter.StatusDone, "session went idle", t0.Add(6*time.Second))) {
		t.Error("subagent idle must not change the parent")
	}
	if p, _ := s.Get("opencode:parent1"); p.Status != adapter.StatusRunning {
		t.Errorf("parent = %s", p.Status)
	}
	if len(s.List()) != 1 {
		t.Errorf("want only the parent workflow, got %d", len(s.List()))
	}
}

func TestLateLinkFoldsExistingChildWorkflow(t *testing.T) {
	// Snapshot replay: status frames (no parent info) come before the
	// session info that carries the link, and the child is already asking.
	s := NewStore()
	t0 := time.Now()
	s.Apply(ev("parent1", adapter.StatusRunning, "working", t0))
	s.Apply(ev("kid1", adapter.StatusRunning, "working", t0))
	s.Apply(askEvent("kid1", question("q1", false, false, "Red", "Blue"), t0))

	meta := child("kid1", "parent1", "", "", t0.Add(time.Second)) // metadata-only session.updated
	s.Apply(meta)

	if _, ok := s.Get("opencode:kid1"); ok {
		t.Fatal("child workflow should have been folded")
	}
	p, _ := s.Get("opencode:parent1")
	if p.Status != adapter.StatusNeedsInput || len(p.Pending) != 1 || p.Pending[0].ID != "q1" {
		t.Fatalf("parent after fold = %+v", p)
	}
}

func TestNestedSubagentsAndCyclesResolve(t *testing.T) {
	s := NewStore()
	t0 := time.Now()
	s.Apply(ev("root", adapter.StatusRunning, "working", t0))
	s.Apply(child("mid", "root", adapter.StatusRunning, "started", t0))
	s.Apply(child("leaf", "mid", adapter.StatusRunning, "started", t0))
	s.Apply(askEvent("leaf", question("q1", false, true), t0))
	if p, _ := s.Get("opencode:root"); len(p.Pending) != 1 {
		t.Fatalf("grandchild ask should reach the root: %+v", p)
	}
	// A link back up the chain is refused rather than looping.
	s.Apply(child("root", "leaf", "", "", t0))
	if id := s.WorkflowFor(ev("leaf", adapter.StatusRunning, "", t0)); id != "opencode:root" {
		t.Errorf("WorkflowFor(leaf) = %q", id)
	}
}
