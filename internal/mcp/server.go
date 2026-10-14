// Package mcp is a minimal Model Context Protocol server: JSON-RPC 2.0 over
// stdio (one JSON message per line), exposing tools only. It implements just
// what an MCP client needs to discover and call tools — initialize, ping,
// tools/list, tools/call — and is stdlib-only like the rest of the module.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// LatestProtocolVersion is offered when the client asks for a version this
// server does not know.
const LatestProtocolVersion = "2025-06-18"

// supportedVersions are protocol revisions whose tools subset this server
// speaks; a client asking for one of them gets it echoed back.
var supportedVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// JSON-RPC error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Annotations are hints to the client about a tool's behaviour.
type Annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   bool   `json:"openWorldHint"`
}

// Tool is one callable tool. InputSchema is a JSON Schema object.
// Handler returns text for the model; an error becomes a tool result with
// isError set (a failure the model should see and react to), not a protocol
// error.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations *Annotations    `json:"annotations,omitempty"`

	Handler func(ctx context.Context, args json.RawMessage) (string, error) `json:"-"`
}

// Server serves tools to one client over a reader/writer pair.
type Server struct {
	Name         string
	Version      string
	Instructions string // optional guidance shown to the model on initialize
	Tools        []Tool

	mu  sync.Mutex // serializes writes
	out io.Writer
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callResult struct {
	Content []textContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// Serve reads requests from in until EOF or ctx is cancelled. Requests are
// handled one at a time, in order; notifications (no id) get no reply.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		s.handle(ctx, line)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (s *Server) handle(ctx context.Context, line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		s.reply(json.RawMessage("null"), nil, &rpcError{codeParseError, "parse error: " + err.Error()})
		return
	}
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"
	if req.JSONRPC != "2.0" || req.Method == "" {
		if !isNotification {
			s.reply(req.ID, nil, &rpcError{codeInvalidRequest, "invalid request"})
		}
		return
	}
	result, rerr := s.dispatch(ctx, req)
	if isNotification {
		return
	}
	s.reply(req.ID, result, rerr)
}

func (s *Server) dispatch(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := LatestProtocolVersion
		if supportedVersions[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
		res := map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]string{"name": s.Name, "version": s.Version},
		}
		if s.Instructions != "" {
			res["instructions"] = s.Instructions
		}
		return res, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		tools := s.Tools
		if tools == nil {
			tools = []Tool{}
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{codeInvalidParams, "invalid params: " + err.Error()}
		}
		for _, t := range s.Tools {
			if t.Name != p.Name {
				continue
			}
			args := p.Arguments
			if len(args) == 0 || string(args) == "null" {
				args = json.RawMessage("{}")
			}
			text, err := t.Handler(ctx, args)
			if err != nil {
				return callResult{Content: []textContent{{"text", err.Error()}}, IsError: true}, nil
			}
			return callResult{Content: []textContent{{"text", text}}}, nil
		}
		return nil, &rpcError{codeInvalidParams, fmt.Sprintf("unknown tool %q", p.Name)}
	}
	if strings.HasPrefix(req.Method, "notifications/") {
		return nil, nil // e.g. notifications/initialized, notifications/cancelled
	}
	return nil, &rpcError{codeMethodNotFound, "method not found: " + req.Method}
}

func (s *Server) reply(id json.RawMessage, result any, rerr *rpcError) {
	resp := response{JSONRPC: "2.0", ID: id, Error: rerr}
	if rerr == nil {
		if result == nil {
			result = map[string]any{}
		}
		resp.Result = result
	}
	b, err := json.Marshal(resp)
	if err != nil {
		b, _ = json.Marshal(response{JSONRPC: "2.0", ID: id, Error: &rpcError{-32603, "internal error: " + err.Error()}})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out.Write(append(b, '\n'))
}

// DecodeArgs unmarshals tool arguments strictly, so a misspelled field is an
// error the model sees instead of being silently ignored.
func DecodeArgs(args json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}
