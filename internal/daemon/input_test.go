package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

// fakeResponder records what the daemon delivers.
type fakeResponder struct {
	answered []adapter.Ask
	answers  [][][]string
	texts    []string
	err      error
}

func (f *fakeResponder) Answer(_ context.Context, _ string, ask adapter.Ask, answers [][]string) error {
	f.answered = append(f.answered, ask)
	f.answers = append(f.answers, answers)
	return f.err
}

func (f *fakeResponder) SendText(_ context.Context, _ string, text string) error {
	f.texts = append(f.texts, text)
	return f.err
}

func question(id string, multiple, free bool, opts ...string) *adapter.Ask {
	return &adapter.Ask{ID: id, Kind: adapter.AskKindQuestion, Questions: []adapter.AskQuestion{
		{Question: "Tabs or spaces?", Options: opts, Multiple: multiple, FreeText: free},
	}}
}

func askEvent(src string, ask *adapter.Ask, at time.Time) adapter.SourceEvent {
	e := ev(src, adapter.StatusNeedsInput, "asked: "+ask.Questions[0].Question, at)
	e.Ask = ask
	e.Capabilities.CanReceiveInput = true
	return e
}

func resolveEvent(src, askID string, at time.Time) adapter.SourceEvent {
	e := ev(src, adapter.StatusRunning, "input received, resuming", at)
	e.Resolves = askID
	e.Capabilities.CanReceiveInput = true
	return e
}

// bound builds a store with one blocked workflow "opencode:ses_abcd1234".
func bound(t *testing.T, ask *adapter.Ask) (*Store, *fakeResponder) {
	t.Helper()
	s, r := NewStore(), &fakeResponder{}
	s.Apply(askEvent("ses_abcd1234", ask, time.Now()))
	s.Bind("opencode:ses_abcd1234", r, "origin-1")
	return s, r
}

func TestPendingAskWinsOverRunning(t *testing.T) {
	s := NewStore()
	t0 := time.Now()
	s.Apply(askEvent("a", question("q1", false, true, "Tabs", "Spaces"), t0))

	// OpenCode keeps reporting busy while blocked; that must not clobber the ask.
	if s.Apply(ev("a", adapter.StatusRunning, "working", t0.Add(time.Second))) {
		t.Error("a running event overwrote a pending ask")
	}
	if w, _ := s.Get("opencode:a"); w.Status != adapter.StatusNeedsInput || len(w.Pending) != 1 {
		t.Fatalf("state = %q pending=%d", w.Status, len(w.Pending))
	}

	// Resolving the only ask returns the workflow to running.
	if !s.Apply(resolveEvent("a", "q1", t0.Add(2*time.Second))) {
		t.Error("resolution should change state")
	}
	if w, _ := s.Get("opencode:a"); w.Status != adapter.StatusRunning || w.Pending != nil {
		t.Errorf("after resolve: %q pending=%v", w.Status, w.Pending)
	}
}

func TestResolvingOneOfTwoAsksStaysNeedsInput(t *testing.T) {
	s := NewStore()
	t0 := time.Now()
	s.Apply(askEvent("a", question("q1", false, true, "x"), t0))
	s.Apply(askEvent("a", question("q2", false, true, "y"), t0.Add(time.Second)))

	s.Apply(resolveEvent("a", "q1", t0.Add(2*time.Second)))
	w, _ := s.Get("opencode:a")
	if w.Status != adapter.StatusNeedsInput || len(w.Pending) != 1 || w.Pending[0].ID != "q2" {
		t.Errorf("status=%q pending=%+v", w.Status, w.Pending)
	}
}

func TestReplayedAskIsIdempotentAndDoneClearsPending(t *testing.T) {
	s := NewStore()
	t0 := time.Now()
	s.Apply(askEvent("a", question("q1", false, true, "x"), t0))
	s.Apply(askEvent("a", question("q1", false, true, "x"), t0.Add(time.Second))) // snapshot replay
	if w, _ := s.Get("opencode:a"); len(w.Pending) != 1 {
		t.Errorf("replayed ask duplicated: %d", len(w.Pending))
	}
	s.Apply(ev("a", adapter.StatusDone, "idle", t0.Add(2*time.Second)))
	if w, _ := s.Get("opencode:a"); w.Pending != nil {
		t.Errorf("done workflow still has pending asks: %+v", w.Pending)
	}
}

func TestAnswerByLabelNumberAndSuffix(t *testing.T) {
	for _, tc := range []struct {
		name, ref, choice, want string
	}{
		{"exact label", "opencode:ses_abcd1234", "Spaces", "Spaces"},
		{"case-insensitive label", "ses_abcd1234", "spaces", "Spaces"},
		{"1-based number", "abcd1234", "1", "Tabs"},
		{"unique suffix", "1234", "2", "Spaces"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r := bound(t, question("q1", false, false, "Tabs", "Spaces"))
			res, err := s.Answer(context.Background(), tc.ref, AnswerRequest{Choice: tc.choice})
			if err != nil {
				t.Fatal(err)
			}
			if got := r.answers[0][0][0]; got != tc.want || res.AskID != "q1" {
				t.Errorf("delivered %q (ask %s), want %q", got, res.AskID, tc.want)
			}
		})
	}
}

func TestAnswerValidation(t *testing.T) {
	ctx := context.Background()

	// Closed question: anything outside the options is refused, nothing delivered.
	s, r := bound(t, question("q1", false, false, "Tabs", "Spaces"))
	if _, err := s.Answer(ctx, "abcd1234", AnswerRequest{Choice: "elastic tabstops"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("closed question accepted free text: %v", err)
	}
	if len(r.answers) != 0 {
		t.Error("invalid answer was delivered")
	}

	// Open question accepts typed text; a number out of range is just text.
	s, r = bound(t, question("q1", false, true, "Tabs", "Spaces"))
	if _, err := s.Answer(ctx, "abcd1234", AnswerRequest{Choice: "9"}); err != nil || r.answers[0][0][0] != "9" {
		t.Errorf("free text: err=%v delivered=%v", err, r.answers)
	}

	// Multiple: comma-separated; single: several answers refused.
	s, r = bound(t, question("q1", true, false, "a", "b", "c"))
	if _, err := s.Answer(ctx, "abcd1234", AnswerRequest{Choice: "1, c"}); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(r.answers[0]); got != "[[a c]]" {
		t.Errorf("multi answers = %s", got)
	}
	s, _ = bound(t, question("q1", false, true, "a", "b"))
	if _, err := s.Answer(ctx, "abcd1234", AnswerRequest{Answers: [][]string{{"a", "b"}}}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("single-answer question took two: %v", err)
	}
}

func TestPermissionAsk(t *testing.T) {
	perm := &adapter.Ask{ID: "per_1", Kind: adapter.AskKindPermission, Questions: []adapter.AskQuestion{
		{Question: "Allow bash?", Options: []string{"once", "always", "reject"}},
	}}
	s, r := bound(t, perm)
	if _, err := s.Answer(context.Background(), "abcd1234", AnswerRequest{Choice: "maybe"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bogus permission reply accepted: %v", err)
	}
	if _, err := s.Answer(context.Background(), "abcd1234", AnswerRequest{Choice: "ONCE"}); err != nil {
		t.Fatal(err)
	}
	if r.answered[0].Kind != adapter.AskKindPermission || r.answers[0][0][0] != "once" {
		t.Errorf("delivered %+v %v", r.answered[0], r.answers[0])
	}
}

func TestAnswerErrors(t *testing.T) {
	ctx := context.Background()
	s, r := bound(t, question("q1", false, true, "x"))

	if _, err := s.Answer(ctx, "nope1234", AnswerRequest{Choice: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown workflow: %v", err)
	}
	if _, err := s.Answer(ctx, "ab", AnswerRequest{Choice: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("too-short ref must not match by suffix: %v", err)
	}
	if _, err := s.Answer(ctx, "abcd1234", AnswerRequest{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("no choice: %v", err)
	}

	// Two asks pending: refuse to guess, accept an explicit ask_id.
	s.Apply(askEvent("ses_abcd1234", question("q2", false, true, "y"), time.Now()))
	if _, err := s.Answer(ctx, "abcd1234", AnswerRequest{Choice: "x"}); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("ambiguous asks: %v", err)
	}
	if res, err := s.Answer(ctx, "abcd1234", AnswerRequest{AskID: "q2", Choice: "y"}); err != nil || res.AskID != "q2" {
		t.Errorf("explicit ask_id: %v %+v", err, res)
	}

	// Delivery failure surfaces as a plain error, not a validation one.
	r.err = errors.New("opencode 404")
	_, err := s.Answer(ctx, "abcd1234", AnswerRequest{AskID: "q1", Choice: "x"})
	if err == nil || errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrNotFound) {
		t.Errorf("delivery failure misclassified: %v", err)
	}

	// Nothing pending / no responder.
	s2 := NewStore()
	s2.Apply(func() adapter.SourceEvent {
		e := ev("idle1234", adapter.StatusRunning, "working", time.Now())
		e.Capabilities.CanReceiveInput = true
		return e
	}())
	if _, err := s2.Answer(ctx, "idle1234", AnswerRequest{Choice: "x"}); !errors.Is(err, ErrCannotReceive) {
		t.Errorf("unbound workflow: %v", err)
	}
	s2.Bind("opencode:idle1234", &fakeResponder{}, "origin-1")
	if _, err := s2.Answer(ctx, "idle1234", AnswerRequest{Choice: "x"}); !errors.Is(err, ErrNoPending) {
		t.Errorf("no pending ask: %v", err)
	}
}

func TestAmbiguousSuffix(t *testing.T) {
	s := NewStore()
	t0 := time.Now()
	s.Apply(ev("ses_aaaa9999", adapter.StatusRunning, "w", t0))
	s.Apply(ev("ses_bbbb9999", adapter.StatusRunning, "w", t0))
	if _, err := s.Resolve("9999"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("shared suffix: %v", err)
	}
	if w, err := s.Resolve("aaaa9999"); err != nil || w.SourceID != "ses_aaaa9999" {
		t.Errorf("unique suffix: %v %v", w.SourceID, err)
	}
}

func TestSendText(t *testing.T) {
	ctx := context.Background()
	s := NewStore()
	e := ev("ses_abcd1234", adapter.StatusDone, "idle", time.Now())
	e.Capabilities.CanReceiveInput = true
	s.Apply(e)
	r := &fakeResponder{}
	s.Bind("opencode:ses_abcd1234", r, "origin-1")

	if err := s.SendText(ctx, "abcd1234", "please add tests"); err != nil || r.texts[0] != "please add tests" {
		t.Fatalf("send: %v %v", err, r.texts)
	}
	if err := s.SendText(ctx, "abcd1234", "   "); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty text: %v", err)
	}

	// Refused while blocked on an ask: answer it, don't talk over it.
	s.Apply(askEvent("ses_abcd1234", question("q1", false, true, "x"), time.Now()))
	if err := s.SendText(ctx, "abcd1234", "hello"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("send while blocked: %v", err)
	}
}
