// Package daemon holds mission-control's own state: the current status of
// every workflow, fed by normalized events from the adapters, plus the HTTP
// surface that exposes it. Adapters never import this package.
package daemon

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
	"github.com/rgolangh/agent-mission-control/internal/procs"
)

// Version is reported by GET /healthz; main sets it from its build version.
var Version = "dev"

// maxHistory bounds the per-workflow transition log. Workflow and parent-link
// limits bound state created by sessions that never send a matching Gone event.
const (
	maxHistory             = 20
	maxWorkflowsPerAdapter = 1000
	maxWorkflowsTotal      = 2000
	maxParentLinks         = 10000
)

// Transition is one change of (status, message) in a workflow's life.
type Transition struct {
	Status  adapter.Status `json:"status"`
	Message string         `json:"message"`
	Rule    string         `json:"rule,omitempty"` // the done/stuck rule that decided it, if one did
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
	LastActivity time.Time            `json:"last_activity"`
	PIDs         []int                `json:"pids,omitempty"` // the agent process and its ancestors, nearest first: a UI matches these to window PIDs to focus the terminal // last frame of any kind from the source, including streaming output
	History      []Transition         `json:"history"`

	input adapter.Responder // how to push input back; nil until an adapter binds one
	hung  bool              // stuck was set by FlagHung, so new activity undoes it
}

// Store is the in-memory workflow table. Safe for concurrent use.
type Store struct {
	mu      sync.RWMutex
	m       map[string]*Workflow
	syncing map[string]map[string]bool // origin -> workflow IDs seen since BeginSync
	parents map[string]string          // child source key -> parent source key (subagents)
}

func NewStore() *Store {
	return &Store{m: make(map[string]*Workflow), syncing: make(map[string]map[string]bool), parents: make(map[string]string)}
}

// maxDepth bounds parent-chain walks, so a bad link can never loop forever.
const maxDepth = 16

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

// WorkflowFor is the workflow an event belongs to after correlation: a
// subagent's events belong to the workflow of the session that spawned it.
// Adapters' input bindings must use this, not WorkflowID.
func (s *Store) WorkflowFor(ev adapter.SourceEvent) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rootLocked(WorkflowID(ev))
}

func (s *Store) rootLocked(id string) string {
	for i := 0; i < maxDepth; i++ {
		p, ok := s.parents[id]
		if !ok {
			return id
		}
		id = p
	}
	return id
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
//   - A source with a Parent (a subagent) is not a workflow of its own: see
//     applyChild.
func (s *Store) Apply(ev adapter.SourceEvent) bool {
	id := WorkflowID(ev)
	if id == "" || ev.Status == adapter.StatusUnknown {
		return false
	}
	ev.Clean() // agent text is stored, and so served, without control characters

	s.mu.Lock()
	defer s.mu.Unlock()

	if ev.Parent != "" {
		if !s.linkLocked(id, ev.Adapter+":"+ev.Parent, ev.Timestamp) {
			return false
		}
	}
	if root := s.rootLocked(id); root != id {
		return s.applyChild(root, ev)
	}

	if ev.Gone {
		_, existed := s.m[id]
		s.deleteWorkflowLocked(id)
		return existed
	}

	w, ok := s.m[id]
	if !ok {
		if ev.Status == "" {
			return false
		}
		if !s.admitWorkflowLocked(ev.Adapter) {
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
	if ev.PID > 0 && (len(w.PIDs) == 0 || w.PIDs[0] != ev.PID) {
		w.PIDs = procs.Ancestors(ev.PID)
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
	w.hung = false
	if ev.Timestamp.After(w.LastActivity) {
		w.LastActivity = ev.Timestamp
	}
	changed := w.transition(ev.Status, ev.Message, ev.Timestamp)
	if changed && ev.Rule != "" {
		w.History[len(w.History)-1].Rule = ev.Rule
	}
	return changed
}

// admitWorkflowLocked keeps both per-adapter and total workflow counts
// bounded. When full, the oldest completed workflow is removed; active,
// waiting and stuck workflows are never evicted to make room.
func (s *Store) admitWorkflowLocked(adapterName string) bool {
	for {
		count := 0
		for _, w := range s.m {
			if w.Adapter == adapterName {
				count++
			}
		}
		if count < maxWorkflowsPerAdapter && len(s.m) < maxWorkflowsTotal {
			return true
		}

		var oldest *Workflow
		for _, w := range s.m {
			if w.Status != adapter.StatusDone && w.Status != adapter.StatusFailed {
				continue
			}
			if count >= maxWorkflowsPerAdapter && w.Adapter != adapterName {
				continue
			}
			if oldest == nil || workflowActivity(w).Before(workflowActivity(oldest)) {
				oldest = w
			}
		}
		if oldest == nil {
			return false
		}
		s.deleteWorkflowLocked(oldest.ID)
	}
}

func workflowActivity(w *Workflow) time.Time {
	if !w.LastActivity.IsZero() {
		return w.LastActivity
	}
	if !w.UpdatedAt.IsZero() {
		return w.UpdatedAt
	}
	return w.StartedAt
}

// deleteWorkflowLocked removes a workflow and links that would otherwise
// retain its ID after eviction, expiry or a source Gone event.
func (s *Store) deleteWorkflowLocked(id string) {
	delete(s.m, id)
	delete(s.parents, id)
	for child, parent := range s.parents {
		if parent == id {
			delete(s.parents, child)
		}
	}
	for _, seen := range s.syncing {
		delete(seen, id)
	}
}

// PruneIdle removes old completed workflows for one adapter. Waiting and
// active workflows are retained regardless of age. Returns removed IDs.
func (s *Store) PruneIdle(adapterName string, now time.Time, idle time.Duration) []string {
	if idle <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed []string
	for id, w := range s.m {
		if w.Adapter != adapterName || (w.Status != adapter.StatusDone && w.Status != adapter.StatusFailed) || len(w.Pending) > 0 {
			continue
		}
		at := workflowActivity(w)
		if at.IsZero() || now.Sub(at) < idle {
			continue
		}
		s.deleteWorkflowLocked(id)
		removed = append(removed, id)
	}
	sort.Strings(removed)
	return removed
}

// Touch records that a source showed signs of life (any frame, even ones
// that carry no state, like streamed output). A workflow FlagHung marked
// stuck goes back to running.
func (s *Store) Touch(id string, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.m[id]
	if !ok {
		return false
	}
	if at.After(w.LastActivity) {
		w.LastActivity = at
	}
	if !w.hung {
		return false
	}
	w.hung = false
	return w.transition(adapter.StatusRunning, "working again", at)
}

// FlagHung marks running workflows that have shown no activity at all for
// longer than after as stuck: a hung provider stream, a tool that never
// returns, or a long command that prints nothing. Anything heard from the
// source undoes it (Touch, or any state event). Workflows waiting on an ask
// or on an unreachable origin are left alone: their silence is explained.
// Returns the IDs it flagged.
func (s *Store) FlagHung(now time.Time, after time.Duration) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var flagged []string
	for id, w := range s.m {
		if w.Status != adapter.StatusRunning || w.Stale || len(w.Pending) > 0 || w.LastActivity.IsZero() {
			continue
		}
		if idle := now.Sub(w.LastActivity); idle > after {
			w.hung = true
			w.transition(adapter.StatusStuck, "no activity for "+idle.Round(time.Minute).String()+": hung, or a long silent command", now)
			flagged = append(flagged, id)
		}
	}
	sort.Strings(flagged)
	return flagged
}

// transition records a (status, message) change and reports whether it was
// one; a repeat only refreshes UpdatedAt.
func (w *Workflow) transition(st adapter.Status, msg string, at time.Time) bool {
	w.UpdatedAt = at
	if w.Status == st && w.Message == msg {
		return false
	}
	w.Status, w.Message = st, msg
	w.History = append(w.History, Transition{Status: st, Message: msg, At: at})
	if len(w.History) > maxHistory {
		w.History = w.History[len(w.History)-maxHistory:]
	}
	return true
}

// linkLocked records that source child was spawned by parent. If child was
// already tracked as a workflow of its own (its first events arrived before
// the link, as in a snapshot replay), it is folded into the root: its
// pending asks move there and its own entry is dropped.
func (s *Store) linkLocked(child, parent string, at time.Time) bool {
	if child == parent || s.parents[child] != "" || s.rootLocked(parent) == child {
		return true
	}
	if len(s.parents) >= maxParentLinks {
		return false
	}
	w, ok := s.m[child]
	root := s.rootLocked(parent)
	if ok && len(w.Pending) > 0 {
		if _, exists := s.m[root]; !exists && !s.admitWorkflowLocked(w.Adapter) {
			return false
		}
	}
	s.parents[child] = parent
	if !ok {
		return true
	}
	delete(s.m, child)
	for _, a := range w.Pending {
		root := s.rootWorkflowLocked(s.rootLocked(child), w.Adapter, w.Capabilities, at)
		if root == nil {
			return false
		}
		root.setAsk(a)
		root.Capabilities = w.Capabilities
		root.transition(adapter.StatusNeedsInput, "subagent "+w.Message, at)
	}
	return true
}

// rootWorkflowLocked returns the workflow with this ID, creating it if a
// subagent's ask arrives before anything from the parent itself.
func (s *Store) rootWorkflowLocked(id, adapterName string, caps adapter.Capabilities, at time.Time) *Workflow {
	if w, ok := s.m[id]; ok {
		return w
	}
	if !s.admitWorkflowLocked(adapterName) {
		return nil
	}
	w := &Workflow{ID: id, Adapter: adapterName, SourceID: strings.TrimPrefix(id, adapterName+":"), Capabilities: caps, StartedAt: at}
	s.m[id] = w
	return w
}

// applyChild handles an event from a subagent whose root workflow is root.
// The parent stays busy for as long as its subagent runs, so the subagent's
// own running/idle/error states add nothing and are dropped. What matters is
// when it blocks on the user: its asks surface on the root workflow (answers
// route by ask ID, so the parent's binding delivers them), and resolving
// them returns the root to running.
func (s *Store) applyChild(root string, ev adapter.SourceEvent) bool {
	if ev.Gone {
		delete(s.parents, WorkflowID(ev))
		return false
	}
	if ev.Ask != nil {
		w := s.rootWorkflowLocked(root, ev.Adapter, ev.Capabilities, ev.Timestamp)
		if w == nil {
			return false
		}
		w.setAsk(*ev.Ask)
		w.Capabilities = ev.Capabilities
		return w.transition(adapter.StatusNeedsInput, "subagent "+ev.Message, ev.Timestamp)
	}
	w, ok := s.m[root]
	if ev.Resolves == "" || !ok {
		return false
	}
	w.dropAsk(ev.Resolves)
	if len(w.Pending) == 0 && w.Status == adapter.StatusNeedsInput {
		return w.transition(adapter.StatusRunning, ev.Message, ev.Timestamp)
	}
	return false
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

// DropOrigin removes every workflow reported by origin: its process has
// exited (a closed terminal tab), so unlike MarkStale there is nothing to
// come back. Returns how many were removed.
func (s *Store) DropOrigin(origin string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, w := range s.m {
		if w.Origin == origin {
			s.deleteWorkflowLocked(id)
			n++
		}
	}
	return n
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
			w.Pending = nil
			w.transition(adapter.StatusDone, "no longer active (stopped while mission-control was disconnected)", now)
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
	c.PIDs = append([]int(nil), w.PIDs...)
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
