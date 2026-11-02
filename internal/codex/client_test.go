package codex

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
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
				_ = wsjson.Write(ctx, conn, map[string]any{"id": m.ID, "result": map[string]any{"data": []string{"test-thread"}}})
			case "thread/resume":
				_ = wsjson.Write(ctx, conn, map[string]any{"id": m.ID, "result": map[string]any{"thread": map[string]any{"id": "test-thread", "status": map[string]any{"type": "active"}}}})
				_ = wsjson.Write(ctx, conn, map[string]any{"id": 0, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "test-thread", "command": "printf test", "availableDecisions": []string{"accept", "cancel"}}})
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
