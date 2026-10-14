package daemon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

// Sentinel errors, so the HTTP layer can pick a status code with errors.Is.
// Anything else returned from here is a failure delivering to the adapter.
var (
	ErrNotFound      = errors.New("no such workflow")
	ErrAmbiguous     = errors.New("ambiguous")
	ErrCannotReceive = errors.New("workflow cannot receive input")
	ErrNoPending     = errors.New("nothing is waiting for an answer")
	ErrInvalidInput  = errors.New("invalid input")
)

// minRefLen keeps a one- or two-character ref from matching arbitrary IDs.
const minRefLen = 4

// AnswerRequest answers one pending ask. Either give Choice (a shorthand for
// single-question asks) or Answers (explicit: one list of strings per
// question, required for multi-question asks).
type AnswerRequest struct {
	AskID   string     `json:"ask_id,omitempty"` // needed only when several asks are pending
	Choice  string     `json:"choice,omitempty"` // option label, or its 1-based number; comma-separated if the question allows several
	Answers [][]string `json:"answers,omitempty"`
}

// AnswerResult reports what was delivered. It does NOT change the workflow's
// state: that only moves when the source's own "answered" event comes back.
type AnswerResult struct {
	WorkflowID string     `json:"workflow_id"`
	AskID      string     `json:"ask_id"`
	Answers    [][]string `json:"answers"`
}

// Resolve finds a workflow by full ID, exact source ID, or a unique suffix of
// the source ID (what `status` prints).
func (s *Store) Resolve(ref string) (Workflow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if w, ok := s.m[ref]; ok {
		return clone(w), nil
	}
	var matches []*Workflow
	for _, w := range s.m {
		if w.SourceID == ref || (len(ref) >= minRefLen && strings.HasSuffix(w.SourceID, ref)) {
			matches = append(matches, w)
		}
	}
	switch len(matches) {
	case 0:
		return Workflow{}, fmt.Errorf("%w: %q", ErrNotFound, ref)
	case 1:
		return clone(matches[0]), nil
	}
	ids := make([]string, len(matches))
	for i, w := range matches {
		ids[i] = w.ID
	}
	return Workflow{}, fmt.Errorf("%w: %q matches %s", ErrAmbiguous, ref, strings.Join(ids, ", "))
}

// Answer delivers an answer to one of a workflow's pending asks through the
// adapter that reported it.
func (s *Store) Answer(ctx context.Context, ref string, req AnswerRequest) (AnswerResult, error) {
	w, err := s.Resolve(ref)
	if err != nil {
		return AnswerResult{}, err
	}
	if err := canReceive(w); err != nil {
		return AnswerResult{}, err
	}

	ask, err := pickAsk(w, req.AskID)
	if err != nil {
		return AnswerResult{}, err
	}

	answers := req.Answers
	switch {
	case answers != nil:
	case req.Choice != "":
		if len(ask.Questions) != 1 {
			return AnswerResult{}, fmt.Errorf("%w: ask has %d questions; pass answers explicitly", ErrInvalidInput, len(ask.Questions))
		}
		answers = [][]string{parseChoice(ask.Questions[0], req.Choice)}
	default:
		return AnswerResult{}, fmt.Errorf("%w: give a choice or answers", ErrInvalidInput)
	}

	answers, err = validateAnswers(ask, answers)
	if err != nil {
		return AnswerResult{}, err
	}
	if err := w.input.Answer(ctx, w.SourceID, ask, answers); err != nil {
		return AnswerResult{}, fmt.Errorf("delivering answer to %s: %w", w.Adapter, err)
	}
	return AnswerResult{WorkflowID: w.ID, AskID: ask.ID, Answers: answers}, nil
}

// SendText sends free text into a workflow's source. It is refused while the
// source is waiting on an ask: that must be answered, not talked over.
func (s *Store) SendText(ctx context.Context, ref, text string) error {
	w, err := s.Resolve(ref)
	if err != nil {
		return err
	}
	if err := canReceive(w); err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("%w: empty text", ErrInvalidInput)
	}
	if len(w.Pending) > 0 {
		return fmt.Errorf("%w: workflow is waiting on %d ask(s); answer them instead", ErrInvalidInput, len(w.Pending))
	}
	if err := w.input.SendText(ctx, w.SourceID, text); err != nil {
		return fmt.Errorf("delivering text to %s: %w", w.Adapter, err)
	}
	return nil
}

func canReceive(w Workflow) error {
	if w.Stale {
		return fmt.Errorf("%w: %s is unreachable (%s)", ErrCannotReceive, w.ID, w.StaleReason)
	}
	if w.input == nil || !w.Capabilities.CanReceiveInput {
		return fmt.Errorf("%w: %s", ErrCannotReceive, w.ID)
	}
	return nil
}

func pickAsk(w Workflow, askID string) (adapter.Ask, error) {
	if len(w.Pending) == 0 {
		return adapter.Ask{}, fmt.Errorf("%w: %s", ErrNoPending, w.ID)
	}
	if askID != "" {
		for _, a := range w.Pending {
			if a.ID == askID {
				return a, nil
			}
		}
		return adapter.Ask{}, fmt.Errorf("%w: no pending ask %q on %s", ErrNoPending, askID, w.ID)
	}
	if len(w.Pending) > 1 {
		ids := make([]string, len(w.Pending))
		for i, a := range w.Pending {
			ids[i] = a.ID
		}
		return adapter.Ask{}, fmt.Errorf("%w: %d asks pending (%s); pass ask_id", ErrAmbiguous, len(ids), strings.Join(ids, ", "))
	}
	return w.Pending[0], nil
}

// parseChoice turns a shorthand choice into answer strings for one question:
// each token is an option's 1-based number or its label (case-insensitive),
// mapped to the canonical label; anything else passes through and is judged
// by validateAnswers. A question that allows several options takes a
// comma-separated list; otherwise the whole string is one answer.
func parseChoice(q adapter.AskQuestion, choice string) []string {
	tokens := []string{choice}
	if q.Multiple {
		tokens = strings.Split(choice, ",")
	}
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if n, err := strconv.Atoi(t); err == nil && n >= 1 && n <= len(q.Options) {
			t = q.Options[n-1]
		}
		out = append(out, canonicalOption(q, t))
	}
	return out
}

func canonicalOption(q adapter.AskQuestion, s string) string {
	for _, o := range q.Options {
		if strings.EqualFold(o, s) {
			return o
		}
	}
	return s
}

// validateAnswers checks answers against the ask's questions and returns them
// with option labels canonicalised. Answers outside a question's options are
// rejected unless the question accepts free text.
func validateAnswers(ask adapter.Ask, answers [][]string) ([][]string, error) {
	if len(answers) != len(ask.Questions) {
		return nil, fmt.Errorf("%w: ask has %d question(s), got %d answer(s)", ErrInvalidInput, len(ask.Questions), len(answers))
	}
	out := make([][]string, len(answers))
	for i, q := range ask.Questions {
		if len(answers[i]) == 0 {
			return nil, fmt.Errorf("%w: question %d has no answer", ErrInvalidInput, i+1)
		}
		if len(answers[i]) > 1 && !q.Multiple {
			return nil, fmt.Errorf("%w: question %d takes a single answer", ErrInvalidInput, i+1)
		}
		out[i] = make([]string, len(answers[i]))
		for j, a := range answers[i] {
			a = canonicalOption(q, strings.TrimSpace(a))
			if a == "" {
				return nil, fmt.Errorf("%w: question %d has an empty answer", ErrInvalidInput, i+1)
			}
			if !q.FreeText && !contains(q.Options, a) {
				return nil, fmt.Errorf("%w: %q is not one of %s", ErrInvalidInput, a, strings.Join(q.Options, " | "))
			}
			out[i][j] = a
		}
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
