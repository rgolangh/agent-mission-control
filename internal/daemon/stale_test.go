package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"mission-control/internal/adapter"
)

// live applies an event and binds the workflow to an origin, as the daemon does.
func live(s *Store, e adapter.SourceEvent, origin string) {
	s.Apply(e)
	s.Bind(WorkflowID(e), &fakeResponder{}, origin)
}

func TestGoneDropsWorkflow(t *testing.T) {
	s := NewStore()
	t0 := time.Now()
	s.Apply(ev("a", adapter.StatusNeedsInput, "asked: x", t0))

	gone := ev("a", adapter.StatusDone, "session deleted", t0.Add(time.Second))
	gone.Gone = true
	if !s.Apply(gone) {
		t.Error("dropping an existing workflow should report a change")
	}
	if _, ok := s.Get("opencode:a"); ok {
		t.Error("deleted session is still listed")
	}
	if s.Apply(gone) {
		t.Error("a second deletion should be a no-op")
	}
}

func TestStaleRefusesInputUntilAnEventProvesReachability(t *testing.T) {
	s, r := bound(t, question("q1", false, true, "x"))
	s.MarkStale("origin-1", "lost connection")

	w, _ := s.Get("opencode:ses_abcd1234")
	if !w.Stale || w.StaleReason != "lost connection" {
		t.Fatalf("not marked stale: %+v", w)
	}
	if w.Status != adapter.StatusNeedsInput || len(w.Pending) != 1 {
		t.Error("last-known state must be kept while stale")
	}
	if _, err := s.Answer(context.Background(), "abcd1234", AnswerRequest{Choice: "x"}); !errors.Is(err, ErrCannotReceive) {
		t.Errorf("answer on a stale workflow: %v", err)
	}
	if len(r.answers) != 0 {
		t.Error("input was delivered to an unreachable origin")
	}

	// Other origins are unaffected.
	s2, _ := bound(t, question("q1", false, true, "x"))
	s2.MarkStale("some-other-origin", "x")
	if w, _ := s2.Get("opencode:ses_abcd1234"); w.Stale {
		t.Error("MarkStale leaked to a different origin")
	}

	// An event from the workflow proves its origin is reachable again.
	s.Bind("opencode:ses_abcd1234", r, "origin-1")
	if w, _ := s.Get("opencode:ses_abcd1234"); w.Stale {
		t.Error("stale flag survived a live event")
	}
}

func TestSyncReconcilesWorkflowsThatStoppedWhileDisconnected(t *testing.T) {
	s := NewStore()
	t0 := time.Now()
	live(s, ev("stillbusy", adapter.StatusRunning, "working", t0), "O1")
	live(s, askEvent("blocked", question("q1", false, true, "x"), t0), "O1")
	live(s, ev("finished", adapter.StatusDone, "idle", t0), "O1")
	live(s, ev("elsewhere", adapter.StatusRunning, "working", t0), "O2")

	s.MarkStale("O1", "lost connection")
	s.BeginSync("O1")
	// Reconnect snapshot: only "stillbusy" is still live on the server.
	live(s, ev("stillbusy", adapter.StatusRunning, "working", t0.Add(time.Minute)), "O1")
	s.EndSync("O1", t0.Add(time.Minute))

	get := func(src string) Workflow { w, _ := s.Get("opencode:" + src); return w }

	if w := get("stillbusy"); w.Status != adapter.StatusRunning || w.Stale {
		t.Errorf("stillbusy: %q stale=%v", w.Status, w.Stale)
	}
	w := get("blocked")
	if w.Status != adapter.StatusDone || w.Pending != nil || w.Stale {
		t.Errorf("blocked (question vanished while disconnected): status=%q pending=%v stale=%v", w.Status, w.Pending, w.Stale)
	}
	if last := w.History[len(w.History)-1]; last.Status != adapter.StatusDone || last.Message == "" {
		t.Errorf("reconcile left no explanation in history: %+v", last)
	}
	if w := get("finished"); w.Status != adapter.StatusDone || w.Stale || len(w.History) != 1 {
		t.Errorf("already-done workflow was touched: %+v", w)
	}
	if w := get("elsewhere"); w.Status != adapter.StatusRunning || w.Stale {
		t.Errorf("another origin's workflow was touched: %+v", w)
	}
}

func TestEndSyncWithoutBeginOnlyClearsStale(t *testing.T) {
	s := NewStore()
	live(s, ev("a", adapter.StatusRunning, "working", time.Now()), "O1")
	s.MarkStale("O1", "x")
	s.EndSync("O1", time.Now())
	if w, _ := s.Get("opencode:a"); w.Stale || w.Status != adapter.StatusRunning {
		t.Errorf("EndSync without BeginSync must not reconcile: %+v", w)
	}
}

func TestDropOriginRemovesOnlyThatOrigin(t *testing.T) {
	s := NewStore()
	t0 := time.Now()
	live(s, ev("a", adapter.StatusRunning, "w", t0), "o1")
	live(s, ev("b", adapter.StatusNeedsInput, "q", t0), "o1")
	live(s, ev("c", adapter.StatusRunning, "w", t0), "o2")
	if n := s.DropOrigin("o1"); n != 2 {
		t.Fatalf("dropped %d, want 2", n)
	}
	if l := s.List(); len(l) != 1 || l[0].SourceID != "c" {
		t.Errorf("left %+v", l)
	}
}
