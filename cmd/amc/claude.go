package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/claudecode"
	"github.com/rgolangh/agent-mission-control/internal/daemon"
)

// claudeHooks ingests Claude Code hook payloads posted by
// `amc hook claude-code`. It answers fast: the hook command is
// holding up Claude while it waits.
func claudeHooks(ctx context.Context, store *daemon.Store, cc *claudecode.Adapter, logf func(string, ...any), logText bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var h claudecode.Hook
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&h); err != nil {
			http.Error(w, "bad hook payload", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		if h.SessionID == "" {
			return
		}
		id := claudecode.Name + ":" + h.SessionID
		if store.Touch(id, time.Now()) {
			logf("%s resumed after being flagged as hung", id)
		}
		ev, ok := cc.Normalize(h)
		if !ok {
			return
		}
		rule := ""
		if ev.TurnEnded {
			if v, err := daemon.JudgeTurn(ctx, cc, &ev); err != nil {
				logf("%s reading last turn: %v", id, err)
			} else {
				rule = "  (rule: " + v.Rule + ")"
			}
		}
		changed := store.Apply(ev)
		store.Bind(id, nil, claudecode.Name) // read-only: no responder
		if changed {
			logf("%s", changeLine(store, id, ev, rule, logText))
		}
	}
}
