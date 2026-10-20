package main

import (
	"strings"
	"testing"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
	"github.com/rgolangh/agent-mission-control/internal/daemon"
)

// The journal persists what the daemon logs, so agent-written text stays out
// of it unless -log-text asks for it (threat model L6).
func TestChangeLineKeepsAgentTextOutByDefault(t *testing.T) {
	store := daemon.NewStore()
	ev := adapter.SourceEvent{Adapter: "opencode", SourceID: "ses_1", Status: adapter.StatusDone,
		Message: "finished: the key is sk-SECRET", Title: "rotate prod creds", Timestamp: time.Now()}
	store.Apply(ev)

	line := changeLine(store, "opencode:ses_1", ev, "  (rule: finished)", false)
	if line != "opencode:ses_1 done  (rule: finished)" {
		t.Errorf("default line = %q", line)
	}
	if line := changeLine(store, "opencode:ses_1", ev, "", true); !strings.Contains(line, "sk-SECRET") || !strings.Contains(line, "[rotate prod creds]") {
		t.Errorf("-log-text line = %q", line)
	}
	gone := adapter.SourceEvent{Adapter: "opencode", SourceID: "ses_1", Gone: true, Message: "session deleted"}
	if line := changeLine(store, "opencode:ses_1", gone, "", false); line != "opencode:ses_1 gone" {
		t.Errorf("gone line = %q", line)
	}
}
