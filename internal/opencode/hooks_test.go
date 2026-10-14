package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Drives SubscribeWithHooks against a fake server through a connect → drop →
// reconnect cycle and checks the lifecycle callbacks fire in order.
func TestHooksFireInOrderAcrossReconnect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.Write([]byte("data: {\"type\":\"server.connected\",\"properties\":{}}\n\n"))
			w.(http.Flusher).Flush()
			<-r.Context().Done() // hold the stream open until the server drops it
		case "/session/status":
			w.Write([]byte(`{"ses_1":{"type":"busy"}}`))
		case "/session/ses_1":
			w.Write([]byte(`{"id":"ses_1","title":"t","directory":"/d"}`))
		default:
			w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()

	var mu sync.Mutex
	var seq []string
	rec := func(s string) { mu.Lock(); seq = append(seq, s); mu.Unlock() }
	count := func(s string) (n int) {
		mu.Lock()
		defer mu.Unlock()
		for _, x := range seq {
			if x == s {
				n++
			}
		}
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(srv.URL).SubscribeWithHooks(ctx, 10*time.Second, Hooks{
			OnEvent: func(e Envelope) {
				if e.Type == EventSessionStatus || e.Type == EventSessionUpdated {
					rec("snapshot:" + e.Type)
				}
			},
			OnSyncStart:  func() { rec("syncstart") },
			OnSynced:     func() { rec("synced") },
			OnDisconnect: func() { rec("disconnect") },
		})
	}()

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for i := 0; i < 100; i++ {
			if cond() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("timed out waiting for %s; saw %v", what, seq)
	}

	waitFor("first sync", func() bool { return count("synced") == 1 })
	srv.CloseClientConnections() // server drops the stream
	waitFor("disconnect", func() bool { return count("disconnect") >= 1 })
	waitFor("second sync after reconnect", func() bool { return count("synced") == 2 })

	cancel()
	<-done
	before := count("disconnect")

	mu.Lock()
	defer mu.Unlock()
	// First cycle must be exactly: syncstart, snapshot frames, synced.
	want := []string{"syncstart", "snapshot:session.status", "snapshot:session.updated", "synced"}
	if len(seq) < len(want) {
		t.Fatalf("sequence too short: %v", seq)
	}
	for i, w := range want {
		if seq[i] != w {
			t.Fatalf("first cycle = %v, want prefix %v", seq, want)
		}
	}
	if before < 1 {
		t.Errorf("no disconnect recorded: %v", seq)
	}
}
