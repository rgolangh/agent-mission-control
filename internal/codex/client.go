package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/rgolangh/agent-mission-control/internal/adapter"
	"github.com/rgolangh/agent-mission-control/internal/daemon"
)

type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}
type pendingAsk struct {
	request   json.RawMessage
	method    string
	questions []string
	turn      string
	reject    string
}
type Client struct {
	Socket      string
	Store       *daemon.Store
	Log         func(string, ...any)
	mu          sync.Mutex
	conn        *websocket.Conn
	calls       map[string]chan message
	asks        map[string]pendingAsk
	turns       map[string]string
	subscribed  map[string]bool
	fileChanges map[string]string
	next        atomic.Int64
}

func New(socket string, store *daemon.Store, log func(string, ...any)) *Client {
	return &Client{Socket: socket, Store: store, Log: log, calls: map[string]chan message{}, asks: map[string]pendingAsk{}, turns: map[string]string{}, subscribed: map[string]bool{}, fileChanges: map[string]string{}}
}
func (c *Client) origin() string { return "codex:" + c.Socket }
func (c *Client) emit(ev adapter.SourceEvent) {
	ev.Adapter = "codex"
	ev.Timestamp = time.Now()
	ev.Capabilities.CanReceiveInput = true
	c.Store.Apply(ev)
	c.Store.Bind("codex:"+ev.SourceID, c, c.origin())
}

func (c *Client) Run(ctx context.Context) {
	var lastLog time.Time
	for ctx.Err() == nil {
		if err := c.run(ctx); err != nil && ctx.Err() == nil {
			if time.Since(lastLog) >= 30*time.Second {
				c.Log("waiting for Codex app-server at %s: %v (retrying)", c.Socket, err)
				lastLog = time.Now()
			}
		}
		c.mu.Lock()
		old := c.asks
		c.asks = map[string]pendingAsk{}
		c.mu.Unlock()
		for key := range old {
			if source, askID, ok := strings.Cut(key, ":"); ok {
				c.emit(adapter.SourceEvent{SourceID: source, Resolves: askID})
			}
		}
		c.Store.MarkStale(c.origin(), "lost connection to Codex app-server")
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
func (c *Client) run(ctx context.Context) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	}}
	conn, _, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(8 << 20)
	c.mu.Lock()
	c.conn = conn
	c.calls = map[string]chan message{}
	c.asks = map[string]pendingAsk{}
	c.turns = map[string]string{}
	c.subscribed = map[string]bool{}
	c.fileChanges = map[string]string{}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		for _, ch := range c.calls {
			close(ch)
		}
		c.calls = map[string]chan message{}
		c.mu.Unlock()
	}()
	done := make(chan error, 1)
	go func() {
		for {
			var m message
			if err := wsjson.Read(ctx, conn, &m); err != nil {
				done <- err
				return
			}
			c.dispatch(m)
		}
	}()
	if _, err = c.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "agent-mission-control", "version": "0.1"}, "capabilities": map[string]any{"experimentalApi": true}}); err != nil {
		return err
	}
	if err = wsjson.Write(ctx, conn, map[string]any{"method": "initialized"}); err != nil {
		return err
	}
	c.Log("connected to Codex app-server at %s", c.Socket)
	c.Store.BeginSync(c.origin())
	if err = c.sync(ctx); err != nil {
		return err
	}
	c.Store.EndSync(c.origin(), time.Now())
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			return err
		case <-tick.C:
			if err := c.sync(ctx); err != nil {
				return err
			}
		}
	}
}
func (c *Client) call(ctx context.Context, method string, params any) (message, error) {
	id := c.next.Add(1)
	key := fmt.Sprint(id)
	ch := make(chan message, 1)
	c.mu.Lock()
	conn := c.conn
	if conn == nil {
		c.mu.Unlock()
		return message{}, fmt.Errorf("Codex disconnected")
	}
	c.calls[key] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.calls, key); c.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return message{}, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return message{}, fmt.Errorf("Codex disconnected")
		}
		if m.Error != nil {
			return m, fmt.Errorf("%s", m.Error.Message)
		}
		return m, nil
	case <-ctx.Done():
		return message{}, ctx.Err()
	}
}
func (c *Client) sync(ctx context.Context) error {
	m, err := c.call(ctx, "thread/loaded/list", map[string]any{})
	if err != nil {
		return err
	}
	var list struct {
		Data []string `json:"data"`
	}
	if err = json.Unmarshal(m.Result, &list); err != nil {
		return err
	}
	for _, id := range list.Data {
		c.mu.Lock()
		known := c.subscribed[id]
		c.mu.Unlock()
		if known {
			continue
		}
		r, err := c.call(ctx, "thread/resume", map[string]any{"threadId": id, "excludeTurns": true})
		if err != nil {
			continue
		}
		var x struct {
			Thread struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Cwd    string `json:"cwd"`
				Status struct {
					Type        string   `json:"type"`
					ActiveFlags []string `json:"activeFlags"`
				} `json:"status"`
			} `json:"thread"`
		}
		if json.Unmarshal(r.Result, &x) != nil {
			continue
		}
		c.mu.Lock()
		c.subscribed[id] = true
		c.mu.Unlock()
		c.Log("connected to Codex session %s", id)
		status := adapter.StatusDone
		if x.Thread.Status.Type == "active" {
			status = adapter.StatusRunning
		}
		c.emit(adapter.SourceEvent{SourceID: id, Title: x.Thread.Name, Location: x.Thread.Cwd, Status: status, Message: "connected"})
	}
	return nil
}
func (c *Client) dispatch(m message) {
	if m.Method == "" && len(m.ID) > 0 {
		c.mu.Lock()
		ch := c.calls[string(m.ID)]
		if ch != nil {
			delete(c.calls, string(m.ID))
			ch <- m
		}
		c.mu.Unlock()
		return
	}
	var p struct {
		ThreadID               string          `json:"threadId"`
		TurnID                 string          `json:"turnId"`
		RequestID              json.RawMessage `json:"requestId"`
		Reason                 string          `json:"reason"`
		Command                string          `json:"command"`
		ItemID                 string          `json:"itemId"`
		GrantRoot              string          `json:"grantRoot"`
		NetworkApprovalContext *struct {
			Host     string `json:"host"`
			Protocol string `json:"protocol"`
		} `json:"networkApprovalContext"`
		AvailableDecisions []json.RawMessage `json:"availableDecisions"`
		TokenUsage         struct {
			Last struct {
				Input  int64 `json:"inputTokens"`
				Cached int64 `json:"cachedInputTokens"`
				Output int64 `json:"outputTokens"`
			} `json:"last"`
		} `json:"tokenUsage"`
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
		Item struct {
			Type     string `json:"type"`
			ID       string `json:"id"`
			Text     string `json:"text"`
			Delivery string `json:"delivery"`
			Changes  []struct {
				Path string `json:"path"`
				Diff string `json:"diff"`
			} `json:"changes"`
			Questions []struct {
				Title   string   `json:"title"`
				Options []string `json:"options"`
			} `json:"questions"`
		} `json:"item"`
		Questions []struct {
			ID       string `json:"id"`
			Header   string `json:"header"`
			Question string `json:"question"`
			IsSecret bool   `json:"isSecret"`
			Options  []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
		Status struct {
			Type        string   `json:"type"`
			ActiveFlags []string `json:"activeFlags"`
		} `json:"status"`
	}
	if json.Unmarshal(m.Params, &p) != nil || p.ThreadID == "" {
		return
	}
	ev := adapter.SourceEvent{SourceID: p.ThreadID}
	switch m.Method {
	case "item/started":
		if p.Item.Type != "fileChange" || len(p.Item.Changes) == 0 {
			return
		}
		var b strings.Builder
		for _, change := range p.Item.Changes {
			if change.Path == "" || change.Diff == "" {
				return
			}
			fmt.Fprintf(&b, "File: %s\n%s\n", change.Path, change.Diff)
		}
		if b.Len() > 32768 {
			return
		}
		c.mu.Lock()
		c.fileChanges[p.ThreadID+":"+p.Item.ID] = b.String()
		c.mu.Unlock()
		return
	case "thread/tokenUsage/updated":
		ev.TokenUsage = &adapter.TokenUsage{Input: p.TokenUsage.Last.Input, CachedInput: p.TokenUsage.Last.Cached, Output: p.TokenUsage.Last.Output, ObservedAt: time.Now()}
	case "turn/started":
		c.mu.Lock()
		c.turns[p.ThreadID] = p.Turn.ID
		c.mu.Unlock()
		ev.Status = adapter.StatusRunning
		ev.Message = "working"
	case "turn/completed":
		c.mu.Lock()
		delete(c.turns, p.ThreadID)
		for key := range c.asks {
			if strings.HasPrefix(key, p.ThreadID+":") {
				delete(c.asks, key)
			}
		}
		c.mu.Unlock()
		ev.Status = adapter.StatusDone
		ev.Message = "turn completed"
		if p.Turn.Status == "failed" {
			ev.Status = adapter.StatusFailed
			ev.Message = "turn failed"
		}
	case "thread/status/changed":
		if p.Status.Type != "active" {
			return
		}
		ev.Status = adapter.StatusRunning
		ev.Message = "working"
		for _, f := range p.Status.ActiveFlags {
			if f == "waitingOnApproval" || f == "waitingOnUserInput" {
				ev.Status = adapter.StatusNeedsInput
				ev.Message = "waiting for input"
			}
		}
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		key := string(m.ID)
		if len(m.ID) == 0 {
			return
		}
		choices := []string{}
		reject := "cancel"
		for _, v := range p.AvailableDecisions {
			var s string
			if json.Unmarshal(v, &s) == nil {
				switch s {
				case "accept":
					choices = append(choices, "once")
				case "acceptForSession":
					choices = append(choices, "always")
				case "decline", "cancel":
					if s == "decline" {
						reject = s
					}
					if !contains(choices, "reject") {
						choices = append(choices, "reject")
					}
				}
			}
		}
		if len(choices) == 0 {
			choices = []string{"once", "reject"}
		}
		desc := strings.TrimSpace(p.Reason + " " + p.Command)
		if m.Method == "item/fileChange/requestApproval" {
			c.mu.Lock()
			diff := c.fileChanges[p.ThreadID+":"+p.ItemID]
			c.mu.Unlock()
			if diff == "" {
				return
			}
			desc = "Approve file changes?\n" + diff
			if p.GrantRoot != "" {
				desc += "\nRequested session write access: " + p.GrantRoot
			}
		}
		if p.NetworkApprovalContext != nil {
			desc = fmt.Sprintf("Network access to %s (%s): %s", p.NetworkApprovalContext.Host, p.NetworkApprovalContext.Protocol, p.Reason)
		}
		if desc == "" {
			desc = "Approve command?"
		}
		ev.Status = adapter.StatusNeedsInput
		ev.Message = "waiting on approval"
		ev.Ask = &adapter.Ask{ID: key, Kind: adapter.AskKindPermission, Questions: []adapter.AskQuestion{{Question: desc, Options: choices}}}
		c.mu.Lock()
		c.asks[p.ThreadID+":"+key] = pendingAsk{request: m.ID, method: m.Method, reject: reject}
		c.mu.Unlock()
	case "item/tool/requestUserInput":
		key := string(m.ID)
		ask := &adapter.Ask{ID: key, Kind: adapter.AskKindQuestion}
		var ids []string
		for _, q := range p.Questions {
			if q.IsSecret {
				return
			}
			aq := adapter.AskQuestion{Question: q.Question, Header: q.Header, FreeText: true}
			for _, o := range q.Options {
				aq.Options = append(aq.Options, o.Label)
			}
			ask.Questions = append(ask.Questions, aq)
			ids = append(ids, q.ID)
		}
		if len(ask.Questions) == 0 {
			return
		}
		ev.Status = adapter.StatusNeedsInput
		ev.Message = "asked: " + ask.Questions[0].Question
		ev.Ask = ask
		c.mu.Lock()
		c.asks[p.ThreadID+":"+key] = pendingAsk{request: m.ID, method: m.Method, questions: ids}
		c.mu.Unlock()
	case "item/completed":
		if p.Item.Type == "fileChange" {
			c.mu.Lock()
			delete(c.fileChanges, p.ThreadID+":"+p.Item.ID)
			c.mu.Unlock()
			return
		}
		if p.Item.Type == "userMessage" {
			c.mu.Lock()
			var resolved []string
			for key, ask := range c.asks {
				if strings.HasPrefix(key, p.ThreadID+":") && ask.method == "async" {
					resolved = append(resolved, strings.TrimPrefix(key, p.ThreadID+":"))
					delete(c.asks, key)
				}
			}
			c.mu.Unlock()
			for _, id := range resolved {
				c.emit(adapter.SourceEvent{SourceID: p.ThreadID, Status: adapter.StatusRunning, Message: "input received", Resolves: id})
			}
			return
		}
		if p.Item.Type != "agentMessage" || p.Item.Delivery != "async" || len(p.Item.Questions) == 0 {
			return
		}
		key := "async/" + p.Item.ID
		ask := &adapter.Ask{ID: key, Kind: adapter.AskKindQuestion}
		for _, q := range p.Item.Questions {
			ask.Questions = append(ask.Questions, adapter.AskQuestion{Question: q.Title, Options: q.Options, FreeText: true})
		}
		ev.Status = adapter.StatusNeedsInput
		ev.Message = "asked: " + ask.Questions[0].Question
		ev.Ask = ask
		c.mu.Lock()
		c.asks[p.ThreadID+":"+key] = pendingAsk{method: "async", turn: p.TurnID}
		c.mu.Unlock()
	case "serverRequest/resolved":
		key := string(p.RequestID)
		ev.Status = adapter.StatusRunning
		ev.Message = "input received"
		ev.Resolves = key
		c.mu.Lock()
		delete(c.asks, p.ThreadID+":"+key)
		c.mu.Unlock()
	default:
		return
	}
	c.emit(ev)
}

func (c *Client) Answer(ctx context.Context, source string, ask adapter.Ask, answers [][]string) error {
	c.mu.Lock()
	p, ok := c.asks[source+":"+ask.ID]
	conn := c.conn
	c.mu.Unlock()
	if !ok || conn == nil {
		return fmt.Errorf("Codex request is no longer pending")
	}
	if p.method == "async" {
		if len(answers) != 1 || len(answers[0]) != 1 {
			return fmt.Errorf("expected one answer")
		}
		_, err := c.call(ctx, "turn/steer", map[string]any{"threadId": source, "expectedTurnId": p.turn, "input": []any{map[string]any{"type": "text", "text": answers[0][0]}}})
		if err == nil {
			c.mu.Lock()
			delete(c.asks, source+":"+ask.ID)
			c.mu.Unlock()
			c.emit(adapter.SourceEvent{SourceID: source, Status: adapter.StatusRunning, Message: "input sent", Resolves: ask.ID})
		}
		return err
	}
	var result any
	if p.method == "item/tool/requestUserInput" {
		out := map[string]any{}
		for i, id := range p.questions {
			if i >= len(answers) {
				return fmt.Errorf("missing answer")
			}
			out[id] = map[string]any{"answers": answers[i]}
		}
		result = map[string]any{"answers": out}
	} else {
		if len(answers) != 1 || len(answers[0]) != 1 {
			return fmt.Errorf("expected one decision")
		}
		d := map[string]string{"once": "accept", "always": "acceptForSession", "reject": p.reject}[answers[0][0]]
		if d == "" {
			return fmt.Errorf("invalid decision")
		}
		result = map[string]any{"decision": d}
	}
	return wsjson.Write(ctx, conn, map[string]any{"id": p.request, "result": result})
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func (c *Client) SendText(ctx context.Context, source, text string) error {
	c.mu.Lock()
	turn := c.turns[source]
	c.mu.Unlock()
	params := map[string]any{"threadId": source, "input": []any{map[string]any{"type": "text", "text": text}}}
	method := "turn/start"
	if turn != "" {
		method = "turn/steer"
		params["expectedTurnId"] = turn
	}
	_, err := c.call(ctx, method, params)
	return err
}
