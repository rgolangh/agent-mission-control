package claudecode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

// tailBytes is how much of a transcript is read. A turn's end, the latest
// todo list and title are near the end; transcripts grow to many MB.
const tailBytes = 512 << 10

// entry is one JSONL transcript line (only what the rules need).
type entry struct {
	Type              string `json:"type"` // user | assistant | system | ai-title | ...
	IsMeta            bool   `json:"isMeta"`
	IsSidechain       bool   `json:"isSidechain"` // subagent traffic
	IsAPIErrorMessage bool   `json:"isApiErrorMessage"`
	AITitle           string `json:"aiTitle"`
	Message           struct {
		StopReason string          `json:"stop_reason"`
		Content    json.RawMessage `json:"content"` // string, or a list of blocks
	} `json:"message"`
}

type block struct {
	Type    string          `json:"type"` // text | thinking | tool_use | tool_result
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	IsError bool            `json:"is_error"`
}

func (e entry) blocks() []block {
	var bs []block
	if json.Unmarshal(e.Message.Content, &bs) == nil {
		return bs
	}
	var s string
	if json.Unmarshal(e.Message.Content, &s) == nil {
		return []block{{Type: "text", Text: s}}
	}
	return nil
}

func (e entry) text() string {
	var parts []string
	for _, b := range e.blocks() {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// isPrompt: a user entry that is something the person typed, not a tool
// result or an injected meta message.
func (e entry) isPrompt() bool {
	if e.Type != "user" || e.IsMeta || e.IsSidechain {
		return false
	}
	for _, b := range e.blocks() {
		if b.Type == "tool_result" {
			return false
		}
	}
	return e.text() != "" && !isInterrupt(e.text())
}

// Claude Code records an Esc/Ctrl-C as a user message with this text
// ("[Request interrupted by user]" or "... for tool use]").
func isInterrupt(s string) bool { return strings.HasPrefix(s, "[Request interrupted by user") }

func readTail(path string, n int64) ([]entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > n {
		if _, err := f.Seek(st.Size()-n, io.SeekStart); err != nil {
			return nil, err
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if len(data) == int(n) { // started mid-line: drop the partial first line
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	var out []entry
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var e entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

func aiTitle(path string) string {
	if path == "" {
		return ""
	}
	entries, err := readTail(path, tailBytes)
	if err != nil {
		return ""
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == "ai-title" && entries[i].AITitle != "" {
			return entries[i].AITitle
		}
	}
	return ""
}

// stopReasons maps Anthropic API stop reasons to TurnEnd.Finish values.
var stopReasons = map[string]string{
	"end_turn":      "stop",
	"stop_sequence": "stop",
	"max_tokens":    "length",
	"tool_use":      "tool-calls",
	"refusal":       "content-filter",
}

// turnFromTranscript extracts the evidence for daemon.Judge: everything
// after the person's last prompt is this turn.
func turnFromTranscript(entries []entry) adapter.TurnEnd {
	var t adapter.TurnEnd
	var main []entry // the main conversation; subagent (sidechain) lines don't end the turn
	for _, e := range entries {
		if !e.IsSidechain && (e.Type == "user" || e.Type == "assistant") {
			main = append(main, e)
		}
	}
	t.TodosTotal, t.TodosOpen = latestTodos(main)

	start := 0
	for i, e := range main {
		if e.isPrompt() {
			start = i + 1
		}
	}
	turn := main[start:]

	var lastAssistant *entry
	for i := range turn {
		e := turn[i]
		switch e.Type {
		case "assistant":
			t.Replied = true
			lastAssistant = &turn[i]
			if e.IsAPIErrorMessage {
				t.Error, t.ErrorMessage = adapter.TurnErrorAPI, e.text()
			} else {
				t.Error, t.ErrorMessage = "", ""
			}
		case "user":
			if isInterrupt(e.text()) {
				t.Error, t.ErrorMessage = adapter.TurnErrorAborted, ""
			}
		}
	}
	if lastAssistant == nil {
		return t
	}
	t.Finish = stopReasons[lastAssistant.Message.StopReason]

	for i := len(turn) - 1; i >= 0 && t.Text == ""; i-- {
		if turn[i].Type == "assistant" && !turn[i].IsAPIErrorMessage {
			t.Text = turn[i].text()
		}
	}
	// A failed tool result with no assistant text after it ended the turn.
	for i := len(turn) - 1; i >= 0; i-- {
		e := turn[i]
		if e.Type == "assistant" && e.text() != "" {
			break
		}
		if e.Type == "user" {
			for _, b := range e.blocks() {
				if b.Type == "tool_result" && b.IsError {
					t.LastToolFailed = toolNameFor(turn[:i])
				}
			}
			if t.LastToolFailed != "" {
				break
			}
		}
	}
	return t
}

// toolNameFor names the most recent tool_use before a tool result.
func toolNameFor(before []entry) string {
	for i := len(before) - 1; i >= 0; i-- {
		for _, b := range before[i].blocks() {
			if b.Type == "tool_use" && b.Name != "" {
				return b.Name
			}
		}
	}
	return "tool"
}

// latestTodos counts the todo list from the most recent TodoWrite call.
func latestTodos(main []entry) (total, open int) {
	for i := len(main) - 1; i >= 0; i-- {
		for _, b := range main[i].blocks() {
			if b.Type != "tool_use" || b.Name != "TodoWrite" {
				continue
			}
			var in struct {
				Todos []struct {
					Status string `json:"status"`
				} `json:"todos"`
			}
			if json.Unmarshal(b.Input, &in) != nil {
				return 0, 0
			}
			for _, td := range in.Todos {
				total++
				if td.Status == "pending" || td.Status == "in_progress" {
					open++
				}
			}
			return total, open
		}
	}
	return 0, 0
}
