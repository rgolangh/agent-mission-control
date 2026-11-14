package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

// Client talks to a running OpenCode server over plain HTTP/SSE — v1
// (`opencode serve`) and v2 (the shared background service) alike. This is
// exactly what the OpenCode TUI and web client themselves talk to.
type Client struct {
	BaseURL  string // e.g. "http://localhost:4096"
	Username string // defaults to "opencode" server-side if a password is set
	Password string // OPENCODE_SERVER_PASSWORD or the v2 service password, empty if server has no auth
	Token    string // bearer token of an agent-mission-control bridge port (see integrations/opencode)
	// TokenFunc, if set, supplies Token lazily: it is called until it
	// returns one (a bridge writes its token file just after opening its port).
	TokenFunc func() string
	tokenMu   sync.Mutex
	// API is the server's major API version: 1 (opencode v1: /global/health,
	// /event, /session/...) or 2 (opencode v2: /api/info, /api/event,
	// /api/session/...). 0 means detect on first use. Discovery sets it when
	// it already knows.
	API   int
	apiMu sync.Mutex
	HTTP  *http.Client
}

const (
	maxJSONResponseBytes       = 4 << 20
	maxSnapshotResponseBytes   = 16 << 20
	maxSnapshotSessions        = 1000
	maxSnapshotPendingRequests = 2000
	maxSnapshotDuration        = 30 * time.Second
	maxSSEEventBytes           = 1 << 20
)

func New(baseURL string) *Client {
	// No overall Timeout (the SSE body streams forever), but bound the wait
	// for response headers so a port that is bound yet not serving fails fast.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 15 * time.Second
	return &Client{BaseURL: baseURL, Username: "opencode", HTTP: &http.Client{Transport: tr}}
}

func (c *Client) authenticate(req *http.Request) {
	c.tokenMu.Lock()
	if c.Token == "" && c.TokenFunc != nil {
		c.Token = c.TokenFunc()
	}
	token := c.Token
	c.tokenMu.Unlock()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if c.Password != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
}

// apiVersion returns the server's major API version, detecting it once:
// only a v2 server answers GET /api/info with a version JSON. The probe is
// authenticated because a v2 service refuses /api/info without credentials
// (a v1 server has no such route and 404s either way).
func (c *Client) apiVersion(ctx context.Context) int {
	c.apiMu.Lock()
	defer c.apiMu.Unlock()
	if c.API != 0 {
		return c.API
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/info", nil)
	if err != nil {
		c.API = 1
		return c.API
	}
	c.authenticate(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		c.API = 1
		return c.API
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		var info struct {
			Version string `json:"version"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&info) == nil && info.Version != "" {
			c.API = 2
			return c.API
		}
	}
	c.API = 1
	return c.API
}

// Health checks the server's identity endpoint before we bother
// subscribing: /global/health on v1, /api/info on v2.
func (c *Client) Health(ctx context.Context) error {
	path := "/global/health"
	if c.apiVersion(ctx) == 2 {
		path = "/api/info"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	c.authenticate(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("opencode server unreachable at %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode health check returned %d", resp.StatusCode)
	}
	return nil
}

// Subscribe opens the server's event stream (GET /event on v1, GET /api/event
// on v2) and pushes decoded Envelopes onto the returned channel until ctx is
// cancelled or the connection drops. It does NOT reconnect on its own — see
// SubscribeWithReconnect for that, because a known OpenCode regression has
// shipped where the stream accepts the connection but silently stops
// forwarding events, and callers should decide their own staleness timeout
// rather than have this hide it.
func (c *Client) Subscribe(ctx context.Context) (<-chan Envelope, <-chan error) {
	events := make(chan Envelope)
	errs := make(chan error, 1)

	go func() {
		defer close(events)
		api := c.apiVersion(ctx)
		stream := "/event"
		if api == 2 {
			stream = "/api/event"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+stream, nil)
		if err != nil {
			errs <- err
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		c.authenticate(req)

		resp, err := c.HTTP.Do(req)
		if err != nil {
			errs <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			errs <- fmt.Errorf("SSE connect returned %d", resp.StatusCode)
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var dataLines [][]byte
		dataBytes := 0
		oversized := false
		emit := func(env Envelope) {
			env.API = api
			select {
			case events <- env:
			case <-ctx.Done():
			}
		}

		flush := func() {
			if oversized {
				dataLines = nil
				dataBytes = 0
				oversized = false
				return
			}
			if len(dataLines) == 0 {
				return
			}
			payload := bytes.Join(dataLines, []byte("\n"))
			dataLines = dataLines[:0]
			dataBytes = 0
			var env Envelope
			if err := json.Unmarshal(payload, &env); err != nil {
				// Not every SSE frame is our event JSON (e.g. the initial
				// server.connected frame or a heartbeat comment) — skip
				// rather than error the whole stream.
				return
			}
			emit(env)
		}

		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				flush()
			case strings.HasPrefix(line, "data:"):
				if oversized {
					continue
				}
				data := []byte(strings.TrimPrefix(line, "data:"))
				n := len(data)
				if len(dataLines) > 0 {
					n++ // newline inserted when the data lines are joined
				}
				if n > maxSSEEventBytes-dataBytes {
					dataLines = nil
					dataBytes = 0
					oversized = true
					continue
				}
				dataLines = append(dataLines, data)
				dataBytes += n
			case strings.HasPrefix(line, ":"):
				// A heartbeat comment (v2 keeps the stream alive with these
				// instead of events). Surface it as a frame the caller can
				// use for liveness; Normalize drops it as noise.
				emit(Envelope{Type: "server.heartbeat"})
			default:
				// ignore "event:", "id:" lines
			}
		}
		flush()
		if err := scanner.Err(); err != nil {
			errs <- err
		} else {
			errs <- fmt.Errorf("SSE stream closed by server")
		}
	}()

	return events, errs
}

// SubscribeWithReconnect wraps Subscribe with backoff and a staleness
// timeout, since /event has been known to accept a connection and then go
// silent without closing it (see events.go comment). onEvent receives every
// raw Envelope (run it through Normalize); onLog gets connection-lifecycle
// diagnostics. The server may not be up yet when this starts, so failures
// retry quietly with a short capped backoff that resets once a connection
// has actually delivered a frame.
func (c *Client) SubscribeWithReconnect(ctx context.Context, staleness time.Duration, onEvent func(Envelope), onLog func(string)) {
	c.SubscribeWithHooks(ctx, staleness, Hooks{OnEvent: onEvent, OnLog: onLog})
}

// Hooks are the callbacks of SubscribeWithHooks. Every field is optional.
type Hooks struct {
	OnEvent func(Envelope) // every raw frame, and every snapshot envelope (run through Normalize)
	OnLog   func(string)   // connection-lifecycle diagnostics

	// OnSyncStart fires when a connection is established, just before the
	// snapshot of current state is replayed through OnEvent; OnSynced fires
	// right after it. Between the two, the daemon can tell which sources are
	// still live on the server.
	OnSyncStart func()
	OnSynced    func()

	// OnDisconnect fires whenever a connection ends or goes stale, and again
	// on each failed retry (so it must be idempotent). It does not fire when
	// ctx is cancelled: the caller already knows.
	OnDisconnect func()
}

// SubscribeWithHooks is SubscribeWithReconnect with lifecycle hooks; it
// blocks until ctx is cancelled.
func (c *Client) SubscribeWithHooks(ctx context.Context, staleness time.Duration, h Hooks) {
	call := func(f func()) {
		if f != nil {
			f()
		}
	}
	onEvent := func(env Envelope) {
		if h.OnEvent != nil {
			h.OnEvent(env)
		}
	}
	onLog := func(msg string) {
		if h.OnLog != nil {
			h.OnLog(msg)
		}
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		connCtx, cancelConn := context.WithCancel(ctx)
		events, errs := c.Subscribe(connCtx)
		connected := false
		last := time.Now()
		ticker := time.NewTicker(staleness / 2)

	inner:
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				cancelConn()
				return
			case env, ok := <-events:
				if !ok {
					select {
					case err := <-errs:
						onLog(fmt.Sprintf("stream error: %v", err))
					default:
					}
					break inner
				}
				last = time.Now()
				if !connected {
					connected = true
					backoff = time.Second
					onLog("connected to /event")
					call(h.OnSyncStart)
					snap, err := c.Snapshot(ctx)
					if err != nil {
						onLog(fmt.Sprintf("snapshot failed (pending asks may be missed): %v", err))
					}
					for _, e := range snap {
						onEvent(e)
					}
					if err == nil {
						call(h.OnSynced)
					}
				}
				onEvent(env)
			case err := <-errs:
				onLog(fmt.Sprintf("stream error: %v", err))
				break inner
			case <-ticker.C:
				if time.Since(last) > staleness {
					onLog(fmt.Sprintf("no events for %s, reconnecting (possible silent-drop regression)", staleness))
					break inner
				}
			}
		}
		ticker.Stop()
		cancelConn() // tear down this connection's goroutine before retrying
		if ctx.Err() == nil {
			call(h.OnDisconnect)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

// get decodes a JSON GET response into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	_, err := c.getLimited(ctx, path, out, maxJSONResponseBytes)
	return err
}

// getLimited decodes one bounded JSON response and reports how many body
// bytes were consumed, so a multi-request snapshot can enforce an aggregate
// budget as well as a per-response limit.
func (c *Client) getLimited(ctx context.Context, path string, out any, limit int64) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("GET %s has no response budget left", path)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return 0, err
	}
	c.authenticate(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("GET %s returned %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return int64(len(body)), err
	}
	if int64(len(body)) > limit {
		return int64(len(body)), fmt.Errorf("GET %s response exceeds %d-byte limit", path, limit)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return int64(len(body)), err
	}
	return int64(len(body)), nil
}

// Snapshot describes what the server is doing right now as synthetic
// Envelopes shaped exactly like live event frames, so Normalize handles them
// unchanged. The event stream only tells you what happens after you
// subscribe; without this, anything pending at (re)connect time — a question
// the agent is already blocked on — would be invisible. Order matters: busy
// sessions first, then metadata, then permission and question asks, because
// a session blocked on an ask still reports "busy" and the ask must win.
// Idle sessions are deliberately not enumerated: only sessions that are busy
// or waiting on you count as live workflows; history stays out.
func (c *Client) Snapshot(ctx context.Context) ([]Envelope, error) {
	if c.apiVersion(ctx) == 2 {
		return c.snapshotV2(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, maxSnapshotDuration)
	defer cancel()
	var out []Envelope
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	budget := int64(maxSnapshotResponseBytes)
	getSnapshot := func(path string, dst any) error {
		limit := int64(maxJSONResponseBytes)
		if budget < limit {
			limit = budget
		}
		n, err := c.getLimited(ctx, path, dst, limit)
		budget -= n
		note(err)
		return err
	}

	var statuses map[string]json.RawMessage
	getSnapshot("/session/status", &statuses)
	ids := make([]string, 0, len(statuses))
	for id := range statuses {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > maxSnapshotSessions {
		note(fmt.Errorf("session status contains %d sessions; snapshot capped at %d", len(ids), maxSnapshotSessions))
		ids = ids[:maxSnapshotSessions]
	}
	for _, id := range ids {
		st := statuses[id]
		props, _ := json.Marshal(map[string]any{"sessionID": id, "status": st})
		out = append(out, Envelope{Type: EventSessionStatus, Properties: props})
	}
	// Titles/directories for the live sessions, as metadata-only session.updated
	// frames. They follow the status frames so the workflow already exists.
	for _, id := range ids {
		var info json.RawMessage
		if err := getSnapshot("/session/"+id, &info); err != nil {
			continue
		}
		props, _ := json.Marshal(map[string]any{"sessionID": id, "info": info})
		out = append(out, Envelope{Type: EventSessionUpdated, Properties: props})
	}

	// GET /permission and GET /question items have the same shape as the
	// properties of permission.asked / question.asked.
	var perms, qs []json.RawMessage
	getSnapshot("/permission", &perms)
	if len(perms) > maxSnapshotPendingRequests {
		note(fmt.Errorf("permission snapshot contains %d requests; capped at %d", len(perms), maxSnapshotPendingRequests))
		perms = perms[:maxSnapshotPendingRequests]
	}
	for _, p := range perms {
		out = append(out, Envelope{Type: EventPermissionAsked, Properties: p})
	}
	getSnapshot("/question", &qs)
	if len(qs) > maxSnapshotPendingRequests {
		note(fmt.Errorf("question snapshot contains %d requests; capped at %d", len(qs), maxSnapshotPendingRequests))
		qs = qs[:maxSnapshotPendingRequests]
	}
	for _, q := range qs {
		out = append(out, Envelope{Type: EventQuestionAsked, Properties: q})
	}
	return out, firstErr
}

// snapshotV2 is the v2 Snapshot: the shared service's equivalents of the v1
// status/ask endpoints, replayed as the v2 event frames Normalize expects.
func (c *Client) snapshotV2(ctx context.Context) ([]Envelope, error) {
	ctx, cancel := context.WithTimeout(ctx, maxSnapshotDuration)
	defer cancel()
	var out []Envelope
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// Running sessions (the only live ones: GET /api/session/active maps
	// session IDs to {type: "running"}).
	var active struct {
		Data map[string]struct {
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/api/session/active", &active); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(active.Data))
	for id := range active.Data {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > maxSnapshotSessions {
		note(fmt.Errorf("session status contains %d sessions; snapshot capped at %d", len(ids), maxSnapshotSessions))
		ids = ids[:maxSnapshotSessions]
	}
	for _, id := range ids {
		props, _ := json.Marshal(map[string]any{
			"sessionID": id,
			"status":    map[string]any{"type": "busy"},
		})
		out = append(out, Envelope{Type: EventSessionStatus, API: 2, Properties: props})
	}

	// Titles/directories/parents for the live sessions, as metadata-only
	// session.renamed frames. They follow the status frames so the workflow
	// already exists.
	dirs := map[string]bool{}
	for _, id := range ids {
		var resp struct {
			Data struct {
				Title    string `json:"title"`
				ParentID string `json:"parentID"`
				Location struct {
					Directory string `json:"directory"`
				} `json:"location"`
			} `json:"data"`
		}
		if err := c.get(ctx, "/api/session/"+id, &resp); err != nil {
			note(err)
			continue
		}
		dirs[resp.Data.Location.Directory] = true
		props, _ := json.Marshal(map[string]any{
			"sessionID": id,
			"title":     resp.Data.Title,
			"directory": resp.Data.Location.Directory,
			"parentID":  resp.Data.ParentID,
		})
		out = append(out, Envelope{Type: EventV2SessionRenamed, API: 2, Properties: props})
	}

	// Pending permission asks, per location (the endpoint is
	// location-scoped; the live sessions' directories cover every location
	// that can have one).
	for dir := range dirs {
		var resp struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := c.get(ctx, "/api/permission/request?location[directory]="+url.QueryEscape(dir), &resp); err != nil {
			note(err)
			continue
		}
		for _, p := range resp.Data {
			out = append(out, Envelope{Type: EventPermissionAsked, API: 2, Properties: p})
		}
	}

	// Pending forms (v2's questions), per session.
	for _, id := range ids {
		var resp struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := c.get(ctx, "/api/session/"+id+"/form", &resp); err != nil {
			note(err)
			continue
		}
		for _, f := range resp.Data {
			props, _ := json.Marshal(map[string]any{"form": json.RawMessage(f)})
			out = append(out, Envelope{Type: EventV2FormCreated, API: 2, Properties: props})
		}
	}
	return out, firstErr
}

// post sends a JSON body to path and treats any non-2xx as an error.
func (c *Client) post(ctx context.Context, path string, body any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.authenticate(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s returned %d", path, resp.StatusCode)
	}
	return nil
}

// PromptAsync pushes text into a session, exactly as if you'd typed it.
// v1: POST /session/{sessionID}/prompt_async (204 on success); the body
// requires a `parts` array; a bare {"text": ...} is rejected.
// v2: POST /api/session/{sessionID}/prompt with {"text": ...}; the server
// admits the prompt into the session's inbox and returns immediately.
func (c *Client) PromptAsync(ctx context.Context, sessionID, text string) error {
	if c.apiVersion(ctx) == 2 {
		return c.post(ctx, fmt.Sprintf("/api/session/%s/prompt", sessionID), map[string]string{"text": text})
	}
	body := map[string]any{
		"parts": []map[string]string{{"type": "text", "text": text}},
	}
	return c.post(ctx, fmt.Sprintf("/session/%s/prompt_async", sessionID), body)
}

// ResolvePermission answers a pending permission ask. reply is one of
// "once" | "always" | "reject". requestID is the `id` from permission.asked.
// v1: POST /permission/{requestID}/reply (the older
// /session/{id}/permissions/{id} route still exists but is deprecated and
// takes {"response": ...} instead). v2: the route lives under the session:
// POST /api/session/{sessionID}/permission/{requestID}/reply.
func (c *Client) ResolvePermission(ctx context.Context, sessionID, requestID, reply string) error {
	if c.apiVersion(ctx) == 2 {
		return c.post(ctx, fmt.Sprintf("/api/session/%s/permission/%s/reply", sessionID, requestID),
			map[string]string{"decision": reply})
	}
	return c.post(ctx, fmt.Sprintf("/permission/%s/reply", requestID), map[string]string{"reply": reply})
}

// ReplyQuestion answers a pending question ask (v1). answers has one entry
// per question, each a list of the selected option labels. requestID is the
// `id` from question.asked. POST /question/{requestID}/reply.
func (c *Client) ReplyQuestion(ctx context.Context, requestID string, answers [][]string) error {
	return c.post(ctx, fmt.Sprintf("/question/%s/reply", requestID), map[string]any{"answers": answers})
}

// ReplyForm answers a pending form (v2's replacement for questions).
// POST /api/session/{sessionID}/form/{formID}/reply with one value per
// field, keyed by the field's key. Chosen labels are mapped back to their
// machine values through the ask's OptionValues; a typed answer (FreeText)
// is sent as-is. Multiselect fields take an array, everything else a single
// string.
func (c *Client) ReplyForm(ctx context.Context, sessionID string, ask adapter.Ask, answers [][]string) error {
	answer := make(map[string]any, len(ask.Questions))
	for i, q := range ask.Questions {
		if i >= len(answers) {
			return fmt.Errorf("form answer for %q is missing (question %d of %d)", q.Key, i+1, len(ask.Questions))
		}
		var vals []string
		for _, chosen := range answers[i] {
			vals = append(vals, optionValue(q, chosen))
		}
		switch {
		case q.Multiple:
			answer[q.Key] = vals
		case len(vals) == 1:
			answer[q.Key] = vals[0]
		default:
			return fmt.Errorf("form field %q takes one answer, got %v", q.Key, answers[i])
		}
	}
	return c.post(ctx, fmt.Sprintf("/api/session/%s/form/%s/reply", sessionID, ask.ID),
		map[string]any{"answer": answer})
}

// optionValue maps a chosen option label to the machine value the tool
// expects, when it distinguishes the two.
func optionValue(q adapter.AskQuestion, label string) string {
	if len(q.OptionValues) == len(q.Options) && len(q.OptionValues) > 0 {
		for i, o := range q.Options {
			if o == label {
				return q.OptionValues[i]
			}
		}
	}
	return label
}
