package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
	"github.com/rgolangh/agent-mission-control/internal/daemon"
)

// Response bodies are copies of real opencode 1.18.30 output, captured while
// the agent was blocked on a question: /session/status still says "busy".
func TestSnapshotReplaysPendingAsksAfterBusy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			w.Write([]byte(`{"ses_1":{"type":"busy"},"ses_2":{"type":"busy"}}`))
		case "/session/ses_1":
			w.Write([]byte(`{"id":"ses_1","title":"Fix login bug","directory":"/home/user/proj"}`))
		case "/session/ses_2":
			w.Write([]byte(`{"id":"ses_2","title":"Write docs","directory":"/home/user/docs"}`))
		case "/permission":
			w.Write([]byte(`[]`))
		case "/question":
			w.Write([]byte(`[{"id":"que_1","sessionID":"ses_1","questions":[{"question":"Tabs or spaces?","header":"Indent","options":[{"label":"Tabs"},{"label":"Spaces"}]}]}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	snap, err := New(srv.URL).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Replay in order through the real store, exactly as the daemon does.
	store := daemon.NewStore()
	for _, env := range snap {
		if ev, ok := Normalize(env); ok {
			store.Apply(ev)
		}
	}
	blocked, _ := store.Get("opencode:ses_1")
	if blocked.Status != adapter.StatusNeedsInput || blocked.Message != "asked: Tabs or spaces?" {
		t.Errorf("ses_1 (blocked on question) = %q %q, want needs_input", blocked.Status, blocked.Message)
	}
	busy, _ := store.Get("opencode:ses_2")
	if busy.Status != adapter.StatusRunning {
		t.Errorf("ses_2 (plain busy) = %q, want running", busy.Status)
	}
	// Titles arrive as metadata-only frames after the status frames.
	if blocked.Title != "Fix login bug" || busy.Title != "Write docs" || busy.Location != "/home/user/docs" {
		t.Errorf("titles/locations = %q %q %q", blocked.Title, busy.Title, busy.Location)
	}
}

func TestSnapshotReportsPartialFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/question" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// /permission returns an object where a list is expected too; either way
	// we must get an error back rather than silently claiming a clean snapshot.
	if _, err := New(srv.URL).Snapshot(context.Background()); err == nil {
		t.Fatal("expected an error when an endpoint fails")
	}
}

func TestSnapshotRejectsOversizedJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat(" ", maxJSONResponseBytes+1)))
	}))
	defer srv.Close()

	if _, err := New(srv.URL).Snapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Snapshot error = %v, want bounded-response error", err)
	}
}

func TestSnapshotCapsNumberOfSessions(t *testing.T) {
	status := make(map[string]any, maxSnapshotSessions+1)
	for i := 0; i <= maxSnapshotSessions; i++ {
		status[fmt.Sprintf("ses_%04d", i)] = map[string]string{"type": "busy"}
	}
	b, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	infoRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session/status":
			_, _ = w.Write(b)
		case strings.HasPrefix(r.URL.Path, "/session/"):
			infoRequests++
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/permission", r.URL.Path == "/question":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	snap, err := New(srv.URL).Snapshot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "snapshot capped") {
		t.Fatalf("Snapshot error = %v, want session-cap error", err)
	}
	if infoRequests != maxSnapshotSessions {
		t.Errorf("session info requests = %d, want %d", infoRequests, maxSnapshotSessions)
	}
	if len(snap) != 2*maxSnapshotSessions {
		t.Errorf("snapshot envelopes = %d, want %d", len(snap), 2*maxSnapshotSessions)
	}
}

func TestSubscribeDropsOversizedSSEFrame(t *testing.T) {
	chunk := strings.Repeat("x", maxSSEEventBytes/2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "data:%s\ndata:%s\n\ndata:{\"type\":%q,\"properties\":{}}\n\n", chunk, chunk, EventSessionIdle)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	events, _ := New(srv.URL).Subscribe(ctx)
	select {
	case env, ok := <-events:
		if !ok {
			t.Fatal("event stream closed before the valid frame")
		}
		if env.Type != EventSessionIdle {
			t.Fatalf("received event type %q, want %q", env.Type, EventSessionIdle)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for valid event after oversized frame")
	}
}
