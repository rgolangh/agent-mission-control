package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// WorkflowList is the body of GET /workflows.
type WorkflowList struct {
	Workflows []Workflow `json:"workflows"`
}

// SendRequest is the body of POST /workflows/{id}/send.
type SendRequest struct {
	Text string `json:"text"`
}

// Handler exposes the store over HTTP. {id} may be a full workflow ID or a
// unique suffix of the source ID, as printed by `mission-control status`.
//
//	GET  /workflows              all workflows, most recently updated first
//	GET  /workflows/{id}         one workflow: pending asks and history
//	POST /workflows/{id}/answer  answer a pending ask   (AnswerRequest)
//	POST /workflows/{id}/send    send free text         (SendRequest)
//	GET  /healthz                liveness
//
// Because this API can act, not just read (it can approve a permission), it
// only serves requests addressed to a loopback host and only accepts POSTs
// with Content-Type: application/json. Together those close the two ways a
// web page in your browser could otherwise drive it: DNS rebinding, and a
// cross-site "simple" POST (which skips CORS preflight unless it is JSON).
// This is not authentication; exposing the API beyond localhost needs that
// first.
func Handler(s *Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /workflows", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, WorkflowList{Workflows: s.List()})
	})
	mux.HandleFunc("GET /workflows/{id}", func(w http.ResponseWriter, r *http.Request) {
		wf, err := s.Resolve(r.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, wf)
	})
	mux.HandleFunc("POST /workflows/{id}/answer", func(w http.ResponseWriter, r *http.Request) {
		var req AnswerRequest
		if !decodeBody(w, r, &req) {
			return
		}
		res, err := s.Answer(r.Context(), r.PathValue("id"), req)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("POST /workflows/{id}/send", func(w http.ResponseWriter, r *http.Request) {
		var req SendRequest
		if !decodeBody(w, r, &req) {
			return
		}
		if err := s.SendText(r.Context(), r.PathValue("id"), req.Text); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	return guard(mux)
}

// Guard applies Handler's loopback-host and JSON-POST rules to another
// handler (e.g. the hook ingestion endpoint, which also changes state).
func Guard(next http.Handler) http.Handler { return guard(next) }

// guard enforces the loopback-host and JSON-POST rules described on Handler.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "requests must be addressed to localhost"})
			return
		}
		if r.Method == http.MethodPost {
			ct := strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0])
			if !strings.EqualFold(ct, "application/json") {
				writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type must be application/json"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return false
	}
	return true
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway // the adapter failed to deliver
	switch {
	case errors.Is(err, ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, ErrAmbiguous), errors.Is(err, ErrNoPending), errors.Is(err, ErrCannotReceive):
		code = http.StatusConflict
	case errors.Is(err, ErrInvalidInput):
		code = http.StatusBadRequest
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// --- client side, used by the CLI ---

// do performs a request against a mission-control API and decodes a 2xx JSON
// body into out (if non-nil). Non-2xx responses become errors carrying the
// server's message.
func do(ctx context.Context, method, url string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("mission-control unreachable (is `mission-control serve` running?): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return errors.New(e.Error)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// FetchWorkflows is the client side of GET /workflows.
func FetchWorkflows(ctx context.Context, baseURL string) ([]Workflow, error) {
	var list WorkflowList
	if err := do(ctx, http.MethodGet, baseURL+"/workflows", nil, &list); err != nil {
		return nil, err
	}
	return list.Workflows, nil
}

// FetchWorkflow is the client side of GET /workflows/{id}.
func FetchWorkflow(ctx context.Context, baseURL, ref string) (Workflow, error) {
	var wf Workflow
	err := do(ctx, http.MethodGet, baseURL+"/workflows/"+ref, nil, &wf)
	return wf, err
}

// PostAnswer is the client side of POST /workflows/{id}/answer.
func PostAnswer(ctx context.Context, baseURL, ref string, req AnswerRequest) (AnswerResult, error) {
	var res AnswerResult
	err := do(ctx, http.MethodPost, baseURL+"/workflows/"+ref+"/answer", req, &res)
	return res, err
}

// PostSend is the client side of POST /workflows/{id}/send.
func PostSend(ctx context.Context, baseURL, ref, text string) error {
	return do(ctx, http.MethodPost, baseURL+"/workflows/"+ref+"/send", SendRequest{Text: text}, nil)
}

// AwaitResolved polls a workflow until askID is no longer pending (the
// source confirmed the answer) or timeout passes. It returns the last state
// seen and whether the confirmation arrived. An answer never moves state by
// itself, so this is how a caller learns what actually happened.
func AwaitResolved(ctx context.Context, baseURL, workflowID, askID string, timeout time.Duration) (Workflow, bool) {
	var last Workflow
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return last, false
		case <-time.After(300 * time.Millisecond):
		}
		wf, err := FetchWorkflow(ctx, baseURL, workflowID)
		if err != nil {
			return last, false
		}
		last = wf
		if !wf.HasPending(askID) {
			return wf, true
		}
	}
	return last, false
}
