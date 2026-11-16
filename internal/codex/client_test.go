package codex

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/rgolangh/agent-mission-control/internal/adapter"
	"github.com/rgolangh/agent-mission-control/internal/daemon"
)

func event(t *testing.T, c *Client, raw string) {
	t.Helper()
	var m message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	c.dispatch(m)
}

func TestApprovalAndResolution(t *testing.T) {
	s := daemon.NewStore()
	c := New("/tmp/codex", s, func(string, ...any) {})
	event(t, c, `{"method":"turn/started","params":{"threadId":"test-thread","turn":{"id":"turn-1"}}}`)
	event(t, c, `{"id":0,"method":"item/commandExecution/requestApproval","params":{"threadId":"test-thread","command":"printf test","availableDecisions":["accept","cancel"]}}`)
	w, err := s.Resolve("codex:test-thread")
	if err != nil {
		t.Fatal(err)
	}
	if w.Status != adapter.StatusNeedsInput || len(w.Pending) != 1 || w.Pending[0].ID != "0" {
		t.Fatalf("unexpected pending approval: %+v", w)
	}
	event(t, c, `{"method":"serverRequest/resolved","params":{"threadId":"test-thread","requestId":0}}`)
	w, _ = s.Resolve("codex:test-thread")
	if len(w.Pending) != 0 || w.Status != adapter.StatusRunning {
		t.Fatalf("approval not cleared: %+v", w)
	}
	if _, ok := c.asks["test-thread:0"]; ok {
		t.Fatal("resolved request remains answerable")
	}
}

func TestFileApprovalRequiresObservedDiff(t *testing.T) {
	s := daemon.NewStore()
	c := New("/tmp/codex", s, func(string, ...any) {})
	event(t, c, `{"method":"turn/started","params":{"threadId":"test-thread","turn":{"id":"turn-1"}}}`)
	request := `{"id":9,"method":"item/fileChange/requestApproval","params":{"threadId":"test-thread","itemId":"edit-1"}}`
	event(t, c, request)
	w, _ := s.Resolve("codex:test-thread")
	if len(w.Pending) != 0 {
		t.Fatal("approval offered without diff")
	}
	event(t, c, `{"method":"item/started","params":{"threadId":"test-thread","item":{"type":"fileChange","id":"edit-1","changes":[{"path":"hello.txt","diff":"+hello"}]}}}`)
	event(t, c, request)
	w, _ = s.Resolve("codex:test-thread")
	if len(w.Pending) != 1 || !strings.Contains(w.Pending[0].Questions[0].Question, "+hello") {
		t.Fatalf("missing file diff: %+v", w.Pending)
	}
}

func TestReconnectReplaysAndExternalAnswerClearsApproval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	accepted := make(chan *websocket.Conn, 2)
	steered := make(chan bool, 1)
	var hidden atomic.Bool
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
		defer conn.CloseNow()
		for {
			var m message
			if wsjson.Read(ctx, conn, &m) != nil {
				return
			}
			switch m.Method {
			case "initialize":
				_ = wsjson.Write(ctx, conn, map[string]any{"id": m.ID, "result": map[string]any{}})
			case "thread/loaded/list":
				ids := []string{"test-thread"}
				if hidden.Load() {
					ids = nil
				}
				_ = wsjson.Write(ctx, conn, map[string]any{"id": m.ID, "result": map[string]any{"data": ids}})
			case "thread/read":
				_ = wsjson.Write(ctx, conn, map[string]any{"id": m.ID, "result": map[string]any{"thread": map[string]any{"id": "test-thread", "source": "cli", "status": map[string]any{"type": "active", "activeFlags": []string{"waitingOnApproval"}}}}})
			case "thread/resume":
				_ = wsjson.Write(ctx, conn, map[string]any{"id": m.ID, "result": map[string]any{"thread": map[string]any{"id": "test-thread", "status": map[string]any{"type": "active"}}}})
				_ = wsjson.Write(ctx, conn, map[string]any{"id": 0, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "test-thread", "command": "printf test", "availableDecisions": []string{"accept", "cancel"}}})
			case "thread/turns/list":
				_ = wsjson.Write(ctx, conn, map[string]any{"id": m.ID, "result": map[string]any{"data": []any{map[string]any{"id": "active-turn", "status": "inProgress"}}}})
			case "turn/steer":
				var params struct {
					Expected string `json:"expectedTurnId"`
				}
				_ = json.Unmarshal(m.Params, &params)
				steered <- params.Expected == "active-turn"
				_ = wsjson.Write(ctx, conn, map[string]any{"id": m.ID, "result": map[string]any{"turnId": "active-turn"}})
			}
		}
	})}
	go srv.Serve(l)
	defer srv.Close()
	s := daemon.NewStore()
	c := New(path, s, func(string, ...any) {})
	go c.Run(ctx)
	first := <-accepted
	waitFor := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("timed out waiting for workflow state")
	}
	waitFor(func() bool { w, e := s.Resolve("codex:test-thread"); return e == nil && len(w.Pending) == 1 })
	first.CloseNow()
	waitFor(func() bool { w, e := s.Resolve("codex:test-thread"); return e == nil && w.Stale })
	second := <-accepted
	waitFor(func() bool {
		w, e := s.Resolve("codex:test-thread")
		return e == nil && !w.Stale && len(w.Pending) == 1
	})
	if err := wsjson.Write(ctx, second, map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": "test-thread", "requestId": 0}}); err != nil {
		t.Fatal(err)
	}
	waitFor(func() bool { w, e := s.Resolve("codex:test-thread"); return e == nil && len(w.Pending) == 0 })
	if _, err := s.Answer(ctx, "codex:test-thread", daemon.AnswerRequest{Choice: "once"}); err == nil {
		t.Fatal("answered request that another client resolved")
	}
	if err := s.SendText(ctx, "codex:test-thread", "hello"); err != nil {
		t.Fatal(err)
	}
	if !<-steered {
		t.Fatal("did not steer the active turn")
	}
	hidden.Store(true)
	if err := c.sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve("codex:test-thread"); err == nil {
		t.Fatal("unloaded session remains listed")
	}
}

func TestStreamingEventsRefreshActivity(t *testing.T) {
	s := daemon.NewStore()
	c := New("/tmp/codex", s, func(string, ...any) {})
	old := time.Now().Add(-time.Hour)
	s.Apply(adapter.SourceEvent{Adapter: "codex", SourceID: "thread", Status: adapter.StatusRunning, Timestamp: old})
	event(t, c, `{"method":"item/agentMessage/delta","params":{"threadId":"thread","delta":"working"}}`)
	w, _ := s.Resolve("codex:thread")
	if !w.LastActivity.After(old) {
		t.Fatal("streaming event did not refresh activity")
	}
}

func TestIdentifySubagent(t *testing.T) {
	for _, tc := range []struct {
		payload string
		want    bool
	}{
		{`{"thread":{"source":"cli","parentThreadId":null}}`, false},
		{`{"thread":{"source":"cli","parentThreadId":"root"}}`, true},
		{`{"thread":{"source":{"subAgent":{"thread_spawn":{}}}}}`, true},
	} {
		if got := isSubagent(json.RawMessage(tc.payload)); got != tc.want {
			t.Errorf("%s: got %v", tc.payload, got)
		}
	}
}
func TestAsyncQuestionAnsweredInTerminal(t *testing.T) {
	s := daemon.NewStore()
	c := New("/tmp/codex", s, func(string, ...any) {})
	event(t, c, `{"method":"turn/started","params":{"threadId":"test-thread","turn":{"id":"turn-1"}}}`)
	event(t, c, `{"method":"item/completed","params":{"threadId":"test-thread","turnId":"turn-1","item":{"type":"agentMessage","id":"question-1","delivery":"async","questions":[{"title":"Tea or coffee?","options":["Tea","Coffee"]}]}}}`)
	w, _ := s.Resolve("codex:test-thread")
	if len(w.Pending) != 1 || w.Pending[0].Questions[0].Question != "Tea or coffee?" {
		t.Fatalf("question missing: %+v", w)
	}
	event(t, c, `{"method":"item/completed","params":{"threadId":"test-thread","item":{"type":"userMessage"}}}`)
	w, _ = s.Resolve("codex:test-thread")
	if len(w.Pending) != 0 || w.Status != adapter.StatusRunning {
		t.Fatalf("question not cleared: %+v", w)
	}
}

func TestTokenUsageDoesNotChangeStatus(t *testing.T) {
	s := daemon.NewStore()
	c := New("/tmp/codex", s, func(string, ...any) {})
	event(t, c, `{"method":"turn/started","params":{"threadId":"test-thread","turn":{"id":"turn-1"}}}`)
	event(t, c, `{"method":"thread/tokenUsage/updated","params":{"threadId":"test-thread","tokenUsage":{"last":{"inputTokens":1000,"cachedInputTokens":800,"outputTokens":42}}}}`)
	w, _ := s.Resolve("codex:test-thread")
	if w.Status != adapter.StatusRunning || w.TokenUsage == nil || w.TokenUsage.CachedInput != 800 || w.TokenUsage.Input != 1000 {
		t.Fatalf("unexpected token usage: %+v", w)
	}
}

// fakeServer is a scripted app-server for one thread whose status the test
// sets. It records the methods called on it.
type fakeServer struct {
	path   string
	status atomic.Value // thread status type
	loaded atomic.Bool
	calls  chan string
}

func startFake(t *testing.T, ctx context.Context, status string) *fakeServer {
	t.Helper()
	f := &fakeServer{path: filepath.Join(t.TempDir(), "app.sock"), calls: make(chan string, 64)}
	f.status.Store(status)
	f.loaded.Store(true)
	l, err := net.Listen("unix", f.path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			var m message
			if wsjson.Read(ctx, conn, &m) != nil {
				return
			}
			if m.Method != "thread/loaded/list" && m.Method != "initialize" && m.Method != "initialized" {
				f.calls <- m.Method
			}
			thread := map[string]any{"id": "t", "name": "demo", "source": "cli", "status": map[string]any{"type": f.status.Load(), "activeFlags": []string{}}}
			var result any = map[string]any{}
			switch m.Method {
			case "thread/loaded/list":
				ids := []string{}
				if f.loaded.Load() {
					ids = []string{"t"}
				}
				result = map[string]any{"data": ids}
			case "thread/read":
				result = map[string]any{"thread": thread}
			case "thread/resume": // subscribing loads the thread
				f.loaded.Store(true)
				result = map[string]any{"thread": thread}
			case "thread/unsubscribe":
				result = map[string]any{"status": "unsubscribed"}
			case "thread/turns/list":
				result = map[string]any{"data": []any{}}
			case "turn/start":
				result = map[string]any{"turn": map[string]any{"id": "turn-2"}}
			}
			if len(m.ID) > 0 {
				_ = wsjson.Write(ctx, conn, map[string]any{"id": m.ID, "result": result})
			}
		}
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return f
}

// expect consumes recorded calls until want arrives; any call in forbidden fails.
func (f *fakeServer) expect(t *testing.T, want string, forbidden ...string) {
	t.Helper()
	timeout := time.After(8 * time.Second)
	for {
		select {
		case got := <-f.calls:
			if got == want {
				return
			}
			if contains(forbidden, got) {
				t.Fatalf("unexpected %s while waiting for %s", got, want)
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

func connect(t *testing.T, ctx context.Context, f *fakeServer) (*Client, *daemon.Store) {
	t.Helper()
	s := daemon.NewStore()
	c := New(f.path, s, func(string, ...any) {})
	go c.Run(ctx)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := s.Resolve("codex:t"); err == nil {
			return c, s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("thread never listed")
	return nil, nil
}

func TestIdleThreadIsListedButNotSubscribed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := startFake(t, ctx, "idle")
	_, s := connect(t, ctx, f)
	w, _ := s.Resolve("codex:t")
	if w.Status != adapter.StatusDone || w.Title != "demo" {
		t.Fatalf("unexpected idle workflow: %+v", w)
	}
	// Idle threads are polled, never resumed: that would keep them loaded.
	f.expect(t, "thread/read")
	f.expect(t, "thread/read", "thread/resume")
	// Once active (say, a prompt typed in the TUI) the next poll subscribes.
	f.status.Store("active")
	f.expect(t, "thread/resume")
}

func TestFinishedThreadIsUnsubscribedAndDroppedOnUnload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := startFake(t, ctx, "active")
	c, s := connect(t, ctx, f)
	f.expect(t, "thread/resume")
	f.status.Store("idle")
	// A notification from the server, delivered as dispatch would see it.
	event(t, c, `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"turn-1","status":"completed"}}}`)
	f.expect(t, "thread/unsubscribe")
	// No client left: the app-server unloads the thread.
	f.loaded.Store(false)
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := s.Resolve("codex:t"); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unloaded thread still listed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSendToIdleThreadResumesFirst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := startFake(t, ctx, "idle")
	_, s := connect(t, ctx, f)
	if err := s.SendText(ctx, "codex:t", "next step"); err != nil {
		t.Fatal(err)
	}
	f.expect(t, "thread/resume", "turn/start")
	f.expect(t, "turn/start")
}

func TestClosedThreadIsDropped(t *testing.T) {
	s := daemon.NewStore()
	c := New("/tmp/codex", s, func(string, ...any) {})
	event(t, c, `{"method":"turn/started","params":{"threadId":"t","turn":{"id":"turn-1"}}}`)
	event(t, c, `{"method":"thread/closed","params":{"threadId":"t"}}`)
	if _, err := s.Resolve("codex:t"); err == nil {
		t.Fatal("closed thread still listed")
	}
	event(t, c, `{"method":"turn/started","params":{"threadId":"u","turn":{"id":"turn-1"}}}`)
	event(t, c, `{"method":"thread/status/changed","params":{"threadId":"u","status":{"type":"notLoaded"}}}`)
	if _, err := s.Resolve("codex:u"); err == nil {
		t.Fatal("unloaded thread still listed")
	}
}
