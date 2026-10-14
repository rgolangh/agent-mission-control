// Package daemon holds mission-control's own state: the current status of
// every workflow, fed by normalized events from the adapters, plus the HTTP
// surface that exposes it. Adapters never import this package.
package daemon

import (
	"sort"
	"sync"
	"time"

	"mission-control/internal/adapter"
)

// maxHistory bounds the per-workflow transition log.
const maxHistory = 20

// Transition is one change of (status, message) in a workflow's life.
type Transition struct {
	Status  adapter.Status `json:"status"`
	Message string         `json:"message"`
	At      time.Time      `json:"at"`
}

// Workflow is the daemon's view of one unit of work. For now every source is
// its own workflow (ID = "<adapter>:<source_id>"); cross-source correlation
// (a PR downstream of a terminal session) will change how IDs are assigned,
// not this shape.
type Workflow struct {
	ID           string               `json:"id"`
	Adapter      string               `json:"adapter"`
	SourceID     string               `json:"source_id"`
	Status       adapter.Status       `json:"status"`
	Message      string               `json:"message"`
	Title        string               `json:"title,omitempty"`
	Location     string               `json:"location,omitempty"`
	Capabilities adapter.Capabilities `json:"capabilities"`
	Pending      []adapter.Ask        `json:"pending,omitempty"` // asks the source is blocked on, oldest first
	Origin       string               `json:"origin,omitempty"`  // which server/instance reports this workflow
	Stale        bool                 `json:"stale,omitempty"`   // the origin is unreachable: state may be out of date and input can't be delivered
	StaleReason  string               `json:"stale_reason,omitempty"`
	StartedAt    time.Time            `json:"started_at"`
	UpdatedAt    time.Time            `json:"updated_at"`
	History      []Transition         `json:"history"`

	input adapter.Responder // how to push input back; nil until an adapter binds one
}

// Store is the in-memory workflow table. Safe for concurrent use.
type Store struct {
	mu      sync.RWMutex
	m       map[string]*Workflow
	syncing map[string]map[string]bool // origin -> workflow IDs seen since BeginSync
}

func NewStore() *Store {
	return &Store{m: make(map[string]*Workflow), syncing: make(map[string]map[string]bool)}
}

// WorkflowID is the workflow an event belongs to: the daemon-assigned
// WorkflowID if set, otherwise one workflow per (adapter, source).
func WorkflowID(ev adapter.SourceEvent) string {
	if ev.WorkflowID != "" {
		return ev.WorkflowID
	}
	if ev.SourceID == "" {
		return ""
	}
	return ev.Adapter + ":" + ev.SourceID
}

// Apply folds one event into the store and reports whether it changed the
// workflow's (status, message). Rules:
//
//   - Events with no source attribution and StatusUnknown events are ignored:
//     an adapter that doesn't understand a frame must not overwrite state it
//     does understand.
//   - An event with an empty Status is metadata-only: it refreshes
//     Title/Location on a workflow that already exists (never creating one, so
//     touching an old session can't add a ghost entry) and leaves status,
//     history and UpdatedAt alone.
//   - Pending asks are tracked from Ask/Resolves. While any ask is pending, a
//     "running" event cannot overwrite needs_input: a source blocked on a
//     question still reports itself busy, and the ask must win. A resolution
//     therefore only returns the workflow to running once nothing else is
//     pending.
//   - A done or failed source has no pending asks.
//   - A Gone event (the source was deleted) drops the workflow.
func (s *Store) Apply(ev adapter.SourceEvent) bool {
	id := WorkflowID(ev)
	if id == "" || ev.Status == adapter.StatusUnknown {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if ev.Gone {
		_, existed := s.m[id]
		delete(s.m, id)
		return existed
	}

	w, ok := s.m[id]
	if !ok {
		if ev.Status == "" {
			return false
		}
		w = &Workflow{ID: id, Adapter: ev.Adapter, SourceID: ev.SourceID, StartedAt: ev.Timestamp}
		s.m[id] = w
	}
	if ev.Title != "" {
		w.Title = ev.Title
	}
	if ev.Location != "" {
		w.Location = ev.Location
	}
	if ev.Ask != nil {
		w.setAsk(*ev.Ask)
	}
	if ev.Resolves != "" {
		w.dropAsk(ev.Resolves)
	}
	if ev.Status == "" {
		return false
	}
	if ev.Status == adapter.StatusDone || ev.Status == adapter.StatusFailed {
		w.Pending = nil
	}
	if ev.Status == adapter.StatusRunning && len(w.Pending) > 0 {
		return false
	}

	w.Capabilities = ev.Capabilities
	w.UpdatedAt = ev.Timestamp

	if ok && w.Status == ev.Status && w.Message == ev.Message {
		return false
	}
	w.Status, w.Message = ev.Status, ev.Message
	w.History = append(w.History, Transition{Status: ev.Status, Message: ev.Message, At: ev.Timestamp})
	if len(w.History) > maxHistory {
		w.History = w.History[len(w.History)-maxHistory:]
	}
	return true
}

// Bind records how to push input back to a workflow's source and which
// origin (server/instance) reports it. Adapters call it for every event they
// publish; it is a no-op for unknown workflows. While a sync is in progress
// for the origin, it also counts the workflow as still alive there.
func (s *Store) Bind(id string, r adapter.Responder, origin string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.m[id]
	if !ok {
		return
	}
	w.input, w.Origin = r, origin
	w.Stale, w.StaleReason = false, "" // an event just arrived, so the origin is reachable
	if seen := s.syncing[origin]; seen != nil {
		seen[id] = true
	}
}

// MarkStale flags every workflow reported by origin as unreachable. Their
// last-known state is kept (it is the best information there is) but is
// marked as possibly out of date, and answer/send are refused.
func (s *Store) MarkStale(origin, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.m {
		if w.Origin == origin {
			w.Stale, w.StaleReason = true, reason
		}
	}
}

// BeginSync starts reconciling an origin after (re)connecting: the adapter is
// about to replay everything that is live on it. Workflows of that origin that
// do not show up before EndSync are no longer live there.
func (s *Store) BeginSync(origin string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncing[origin] = make(map[string]bool)
}

// EndSync finishes the reconcile started by BeginSync. The replay only lists
// live sessions (busy or waiting on an ask), so any workflow of this origin
// still shown as running or needs_input that was not replayed has since
// stopped, and we missed the transition while disconnected: it is set to done
// with an explanatory message and its pending asks are dropped. Every
// workflow of the origin is then marked reachable again.
func (s *Store) EndSync(origin string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := s.syncing[origin]
	delete(s.syncing, origin)
	for id, w := range s.m {
		if w.Origin != origin {
			continue
		}
		w.Stale, w.StaleReason = false, ""
		if seen == nil || seen[id] {
			continue
		}
		if w.Status == adapter.StatusRunning || w.Status == adapter.StatusNeedsInput {
			const msg = "no longer active (stopped while mission-control was disconnected)"
			w.Status, w.Message, w.Pending, w.UpdatedAt = adapter.StatusDone, msg, nil, now
			w.History = append(w.History, Transition{Status: adapter.StatusDone, Message: msg, At: now})
			if len(w.History) > maxHistory {
				w.History = w.History[len(w.History)-maxHistory:]
			}
		}
	}
}

func (w *Workflow) setAsk(a adapter.Ask) {
	for i := range w.Pending {
		if w.Pending[i].ID == a.ID {
			w.Pending[i] = a
			return
		}
	}
	w.Pending = append(w.Pending, a)
}

func (w *Workflow) dropAsk(id string) {
	kept := w.Pending[:0]
	for _, a := range w.Pending {
		if a.ID != id {
			kept = append(kept, a)
		}
	}
	w.Pending = kept
	if len(w.Pending) == 0 {
		w.Pending = nil
	}
}

// List returns a snapshot of all workflows, most recently updated first.
func (s *Store) List() []Workflow {
	s.mu.RLock()
	out := make([]Workflow, 0, len(s.m))
	for _, w := range s.m {
		out = append(out, clone(w))
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// Get returns a snapshot of one workflow by exact ID.
func (s *Store) Get(id string) (Workflow, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, ok := s.m[id]
	if !ok {
		return Workflow{}, false
	}
	return clone(w), true
}

func clone(w *Workflow) Workflow {
	c := *w
	c.History = append([]Transition(nil), w.History...)
	c.Pending = append([]adapter.Ask(nil), w.Pending...)
	return c
}

// HasPending reports whether the ask with this ID is still waiting.
func (w Workflow) HasPending(askID string) bool {
	for _, a := range w.Pending {
		if a.ID == askID {
			return true
		}
	}
	return false
}
