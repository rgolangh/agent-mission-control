package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// run feeds lines to a server and returns the decoded responses, in order.
func run(t *testing.T, s *Server, lines ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var resps []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad response line %q: %v", sc.Text(), err)
		}
		resps = append(resps, m)
	}
	return resps
}

func echoServer() *Server {
	return &Server{
		Name: "t", Version: "1", Instructions: "hi",
		Tools: []Tool{{
			Name:        "echo",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: func(_ context.Context, args json.RawMessage) (string, error) {
				var a struct{ Say string }
				if err := DecodeArgs(args, &a); err != nil {
					return "", err
				}
				if a.Say == "fail" {
					return "", errors.New("boom")
				}
				return a.Say, nil
			},
		}},
	}
}

func TestHandshakeAndToolCalls(t *testing.T) {
	r := run(t, echoServer(),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"c","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"Say":"hello"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","arguments":{"Say":"fail"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"echo","arguments":{"Bogus":1}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"nope"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":"p","method":"ping"}`,
		`not json`,
	)
	if len(r) != 9 { // the notification gets no reply
		t.Fatalf("got %d responses: %v", len(r), r)
	}
	init := r[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" || init["instructions"] != "hi" {
		t.Errorf("initialize: %v", init)
	}
	if tools := r[1]["result"].(map[string]any)["tools"].([]any); len(tools) != 1 {
		t.Errorf("tools/list: %v", tools)
	}
	text := func(m map[string]any) (string, bool) {
		res := m["result"].(map[string]any)
		isErr, _ := res["isError"].(bool)
		return res["content"].([]any)[0].(map[string]any)["text"].(string), isErr
	}
	if s, isErr := text(r[2]); s != "hello" || isErr {
		t.Errorf("call: %q %v", s, isErr)
	}
	if s, isErr := text(r[3]); s != "boom" || !isErr {
		t.Errorf("tool error should be an isError result: %q %v", s, isErr)
	}
	if s, isErr := text(r[4]); !isErr || !strings.Contains(s, "unknown field") {
		t.Errorf("unknown argument should be rejected: %q %v", s, isErr)
	}
	code := func(m map[string]any) float64 { return m["error"].(map[string]any)["code"].(float64) }
	if code(r[5]) != codeInvalidParams || code(r[6]) != codeMethodNotFound || code(r[8]) != codeParseError {
		t.Errorf("error codes: %v %v %v", r[5], r[6], r[8])
	}
	if r[7]["id"] != "p" {
		t.Errorf("ping must echo a string id: %v", r[7])
	}
}

func TestUnknownProtocolVersionGetsLatest(t *testing.T) {
	r := run(t, echoServer(), `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if v := r[0]["result"].(map[string]any)["protocolVersion"]; v != LatestProtocolVersion {
		t.Errorf("got %v", v)
	}
}
