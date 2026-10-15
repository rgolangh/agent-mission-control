package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

func hook(t *testing.T, js string) Hook {
	t.Helper()
	var h Hook
	if err := json.Unmarshal([]byte(js), &h); err != nil {
		t.Fatal(err)
	}
	return h
}

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const (
	prompt    = `{"type":"user","message":{"role":"user","content":"do the thing"}}`
	toolUse   = `{"type":"assistant","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`
	toolOK    = `{"type":"user","message":{"content":[{"type":"tool_result","content":"a b"}]}}`
	toolError = `{"type":"user","message":{"content":[{"type":"tool_result","content":"boom","is_error":true}]}}`
)

func answer(text string) string {
	b, _ := json.Marshal(text)
	return `{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"thinking","thinking":"x"},{"type":"text","text":` + string(b) + `}]}}`
}

// Hook payloads as captured from Claude Code 2.1.296 (trimmed).
func TestNormalizeLifecycle(t *testing.T) {
	tp := writeTranscript(t, `{"type":"ai-title","aiTitle":"List files","sessionId":"s1"}`, prompt, toolUse, toolOK, answer("There are 2 entries."))
	a := New()
	a.ProjectsDirs = []string{filepath.Dir(tp)}
	steps := []struct {
		js     string
		ok     bool
		status adapter.Status
	}{
		{`{"session_id":"s1","transcript_path":"` + tp + `","cwd":"/w","hook_event_name":"SessionStart","source":"startup"}`, true, adapter.StatusDone},
		{`{"session_id":"s1","transcript_path":"` + tp + `","cwd":"/w","hook_event_name":"UserPromptSubmit","prompt":"Run ls\nplease","permission_mode":"default"}`, true, adapter.StatusRunning},
		{`{"session_id":"s1","transcript_path":"` + tp + `","cwd":"/w","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"toolu_1","tool_input":{"command":"ls"}}`, true, adapter.StatusRunning},
		{`{"session_id":"s1","transcript_path":"` + tp + `","cwd":"/w","hook_event_name":"SubagentStop"}`, false, ""},
		{`{"session_id":"s1","transcript_path":"` + tp + `","cwd":"/w","hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"There are 2 entries."}`, true, adapter.StatusDone},
	}
	for i, st := range steps {
		ev, ok := a.Normalize(hook(t, st.js))
		if ok != st.ok || ev.Status != st.status {
			t.Fatalf("step %d: ok=%v status=%q, want %v %q", i, ok, ev.Status, st.ok, st.status)
		}
		if ok && (ev.Adapter != Name || ev.SourceID != "s1" || ev.Location != "/w" || ev.Capabilities.CanReceiveInput) {
			t.Errorf("step %d: bad attribution %+v", i, ev)
		}
		if i == 0 && ev.Title != "List files" {
			t.Errorf("ai-title should label the session, got %q", ev.Title)
		}
		if i == 4 && !ev.TurnEnded {
			t.Error("Stop must end the turn")
		}
	}
	turn, err := a.LastTurn(context.Background(), "s1")
	if err != nil || !turn.Replied || turn.Finish != "stop" || turn.Text != "There are 2 entries." {
		t.Errorf("LastTurn = %+v, %v", turn, err)
	}
	ev, _ := a.Normalize(hook(t, `{"session_id":"s1","hook_event_name":"SessionEnd","reason":"prompt_input_exit"}`))
	if !ev.Gone {
		t.Error("SessionEnd must drop the workflow")
	}
}

func TestExpireIdleSessions(t *testing.T) {
	a := New()
	for _, h := range []Hook{
		{SessionID: "old", Event: "Stop"},
		{SessionID: "recent", Event: "Stop"},
		{SessionID: "live", Event: "Stop", AgentPID: os.Getpid()},
	} {
		if _, ok := a.Normalize(h); !ok {
			t.Fatalf("session %s was not normalized", h.SessionID)
		}
	}
	now := time.Now()
	a.mu.Lock()
	a.sessions["old"].lastSeen = now.Add(-48 * time.Hour)
	a.sessions["recent"].lastSeen = now.Add(-time.Hour)
	a.sessions["live"].lastSeen = now.Add(-48 * time.Hour)
	a.mu.Unlock()

	got := a.ExpireIdle(now, 24*time.Hour)
	if len(got) != 1 || got[0] != "old" {
		t.Fatalf("expired = %v, want [old]", got)
	}
	if len(a.sessions) != 2 || a.sessions["recent"] == nil || a.sessions["live"] == nil {
		t.Fatalf("remaining sessions = %v", a.sessions)
	}
}

func TestNormalizeBoundsSessionState(t *testing.T) {
	a := New()
	for i := 0; i < maxSessions; i++ {
		if _, ok := a.Normalize(Hook{SessionID: fmt.Sprintf("s-%d", i), Event: "Stop"}); !ok {
			t.Fatalf("session %d was rejected before the limit", i)
		}
	}
	if _, ok := a.Normalize(Hook{SessionID: "over-limit", Event: "Stop"}); ok {
		t.Error("session was accepted after the hard limit")
	}
	if len(a.sessions) != maxSessions {
		t.Errorf("adapter session count = %d, want %d", len(a.sessions), maxSessions)
	}
}

// Seen live: Stop runs before the final message reaches the transcript.
func TestStopTextWinsOverUnflushedTranscript(t *testing.T) {
	tp := writeTranscript(t, prompt, toolUse, toolOK) // final answer not written yet
	a := New()
	a.ProjectsDirs = []string{filepath.Dir(tp)}
	a.Normalize(hook(t, `{"session_id":"s4","transcript_path":"`+tp+`","hook_event_name":"UserPromptSubmit","prompt":"x"}`))
	a.Normalize(hook(t, `{"session_id":"s4","transcript_path":"`+tp+`","hook_event_name":"Stop","last_assistant_message":"Go or Rust?"}`))
	turn, err := a.LastTurn(context.Background(), "s4")
	if err != nil || !turn.Replied || turn.Text != "Go or Rust?" || turn.Finish != "stop" {
		t.Fatalf("turn = %+v, %v", turn, err)
	}
	// The next prompt starts a new turn: the old Stop text must not leak into it.
	a.Normalize(hook(t, `{"session_id":"s4","transcript_path":"`+tp+`","hook_event_name":"UserPromptSubmit","prompt":"y"}`))
	if turn, _ = a.LastTurn(context.Background(), "s4"); turn.Text == "Go or Rust?" {
		t.Errorf("stale Stop text reused: %+v", turn)
	}
}

func TestPromptTitlesUntilAITitle(t *testing.T) {
	a := New()
	ev, _ := a.Normalize(hook(t, `{"session_id":"s2","hook_event_name":"UserPromptSubmit","prompt":"Fix the flaky login test in auth/ and make sure CI is green before you stop"}`))
	if ev.Title != "Fix the flaky login test in auth/ and make sure CI is green…" {
		t.Errorf("title from prompt = %q", ev.Title)
	}
	ev, _ = a.Normalize(hook(t, `{"session_id":"s2","hook_event_name":"UserPromptSubmit","prompt":"second prompt"}`))
	if ev.Title != "" {
		t.Errorf("a later prompt must not rename the session, got %q", ev.Title)
	}
}

func TestAskUserQuestionAndPermissions(t *testing.T) {
	a := New()
	ev, _ := a.Normalize(hook(t, `{"session_id":"s3","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_use_id":"toolu_q",
		"tool_input":{"questions":[{"question":"Which DB?","header":"Storage","multiSelect":false,"options":[{"label":"Postgres","description":"x"},{"label":"SQLite","description":"y"}]}]}}`))
	if ev.Status != adapter.StatusNeedsInput || ev.Ask == nil || ev.Ask.ID != "toolu_q" || ev.Message != "asked: Which DB?" ||
		len(ev.Ask.Questions) != 1 || strings.Join(ev.Ask.Questions[0].Options, ",") != "Postgres,SQLite" || !ev.Ask.Questions[0].FreeText {
		t.Fatalf("question = %+v / %+v", ev, ev.Ask)
	}
	ev, _ = a.Normalize(hook(t, `{"session_id":"s3","hook_event_name":"PostToolUse","tool_name":"AskUserQuestion","tool_use_id":"toolu_q"}`))
	if ev.Resolves != "toolu_q" || ev.Status != adapter.StatusRunning {
		t.Errorf("answered = %+v", ev)
	}

	ev, _ = a.Normalize(hook(t, `{"session_id":"s3","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf build"}}`))
	if ev.Status != adapter.StatusNeedsInput || ev.Message != "waiting on approval: Bash: rm -rf build" {
		t.Errorf("permission request = %+v", ev)
	}
	ev, ok := a.Normalize(hook(t, `{"session_id":"s3","hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}`))
	if !ok || ev.Status != adapter.StatusNeedsInput {
		t.Errorf("permission notification = %+v", ev)
	}
	ev, ok = a.Normalize(hook(t, `{"session_id":"s3","hook_event_name":"Notification","notification_type":"idle_prompt","message":"Claude is waiting for your input"}`))
	if !ok || !ev.TurnEnded {
		t.Errorf("idle notification should end the turn (covers interrupts, which fire no Stop): %+v", ev)
	}
	if _, ok = a.Normalize(hook(t, `{"session_id":"s3","hook_event_name":"Notification","notification_type":"auth_success","message":"ok"}`)); ok {
		t.Error("unrelated notifications carry no state")
	}
}

func TestTurnFromTranscript(t *testing.T) {
	parse := func(lines ...string) adapter.TurnEnd {
		t.Helper()
		es, err := readTail(writeTranscript(t, lines...), tailBytes)
		if err != nil {
			t.Fatal(err)
		}
		return turnFromTranscript(es)
	}

	got := parse(answer("old answer"), prompt, toolUse, toolOK, answer("Which port?"))
	if !got.Replied || got.Text != "Which port?" || got.Finish != "stop" || got.Error != "" {
		t.Errorf("normal: %+v", got)
	}
	if got = parse(answer("old"), prompt); got.Replied {
		t.Errorf("no reply after the prompt: %+v", got)
	}
	got = parse(prompt, toolUse, `{"type":"user","message":{"content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`)
	if got.Error != adapter.TurnErrorAborted {
		t.Errorf("interrupt: %+v", got)
	}
	got = parse(prompt, `{"type":"assistant","isApiErrorMessage":true,"message":{"content":[{"type":"text","text":"API Error: 529 overloaded"}]}}`)
	if got.Error != adapter.TurnErrorAPI || got.ErrorMessage != "API Error: 529 overloaded" {
		t.Errorf("api error: %+v", got)
	}
	got = parse(prompt, toolUse, toolError)
	if got.LastToolFailed != "Bash" || got.Finish != "tool-calls" {
		t.Errorf("failed tool last: %+v", got)
	}
	got = parse(prompt, `{"type":"assistant","message":{"stop_reason":"max_tokens","content":[{"type":"text","text":"Here is the start of"}]}}`)
	if got.Finish != "length" {
		t.Errorf("max_tokens: %+v", got)
	}
	got = parse(prompt, `{"type":"assistant","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","name":"TodoWrite","input":{"todos":[{"content":"a","status":"completed"},{"content":"b","status":"in_progress"},{"content":"c","status":"pending"}]}}]}}`, toolOK, answer("Pausing."))
	if got.TodosTotal != 3 || got.TodosOpen != 2 {
		t.Errorf("todos: %+v", got)
	}
	// Subagent (sidechain) traffic and tool results are not the person's prompt.
	got = parse(prompt, `{"type":"user","isSidechain":true,"message":{"content":"subagent task"}}`, `{"type":"assistant","isSidechain":true,"message":{"stop_reason":"end_turn","content":[{"type":"text","text":"sub done"}]}}`, answer("All done."))
	if got.Text != "All done." {
		t.Errorf("sidechain: %+v", got)
	}
}

func TestReadTailDropsPartialLine(t *testing.T) {
	long := `{"type":"user","message":{"content":"` + strings.Repeat("x", 300) + `"}}`
	p := writeTranscript(t, long, answer("end"))
	es, err := readTail(p, 200) // the last line fits, the first only partly
	if err != nil || len(es) != 1 || es[0].text() != "end" {
		t.Errorf("tail = %d entries, err %v", len(es), err)
	}
}

// Security: transcript_path comes from the (forgeable) hook payload.
func TestTranscriptPathMustBeInsideProjectsDir(t *testing.T) {
	projects := t.TempDir()
	inside := filepath.Join(projects, "-home-x", "s.jsonl")
	os.MkdirAll(filepath.Dir(inside), 0o700)
	os.WriteFile(inside, []byte(answer("ok")+"\n"), 0o600)
	outside := writeTranscript(t, answer("SECRET"))
	link := filepath.Join(projects, "link.jsonl")
	os.Symlink(outside, link)
	notJSONL := filepath.Join(projects, "notes.txt")
	os.WriteFile(notJSONL, []byte("x"), 0o600)

	a := New()
	a.ProjectsDirs = []string{projects}
	for _, c := range []struct {
		path string
		ok   bool
	}{
		{inside, true},
		{outside, false},
		{filepath.Join(projects, "..", filepath.Base(filepath.Dir(outside)), "s.jsonl"), false},
		{link, false}, // symlink inside pointing outside
		{notJSONL, false},
		{"relative/s.jsonl", false},
	} {
		if _, ok := a.transcriptPath(c.path); ok != c.ok {
			t.Errorf("%s: accepted=%v, want %v", c.path, ok, c.ok)
		}
	}
	a.Normalize(hook(t, `{"session_id":"x","hook_event_name":"Stop","transcript_path":"`+outside+`"}`))
	if _, err := a.LastTurn(context.Background(), "x"); err == nil {
		t.Error("a rejected transcript_path must not be read")
	}
}

func TestMinimizeDropsContent(t *testing.T) {
	big := strings.Repeat("y", 20000)
	in := map[string]any{
		"session_id": "s", "hook_event_name": "PostToolUse", "tool_name": "Write", "cwd": "/w",
		"tool_input":             map[string]any{"file_path": "/w/a.go", "content": "SECRET FILE BODY"},
		"tool_response":          map[string]any{"stdout": "SECRET OUTPUT"},
		"prompt":                 strings.Repeat("p", 1000),
		"last_assistant_message": "FIRST LINE\n" + big + "\nWhich one?",
		"unknown_future_field":   "x",
	}
	out := Minimize(in)
	b, _ := json.Marshal(out)
	js := string(b)
	for _, leak := range []string{"SECRET FILE BODY", "SECRET OUTPUT", "tool_response", "unknown_future_field"} {
		if strings.Contains(js, leak) {
			t.Errorf("minimized payload still contains %q", leak)
		}
	}
	if !strings.Contains(js, "/w/a.go") || len(out["prompt"].(string)) != maxPromptBytes {
		t.Errorf("kept fields wrong: %s", js[:200])
	}
	last := out["last_assistant_message"].(string)
	if !strings.HasPrefix(last, "FIRST LINE") || !strings.HasSuffix(last, "Which one?") || len(last) > maxFinalHead+maxFinalTail+8 {
		t.Errorf("final message should keep head and tail, len %d", len(last))
	}
	q := Minimize(map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{"q"}}})
	if q["tool_input"].(map[string]any)["questions"] == nil {
		t.Error("AskUserQuestion input must survive")
	}
}

func TestLocalSlashCommandsAreNotTurns(t *testing.T) {
	es, err := readTail(writeTranscript(t, prompt, answer("Done."),
		`{"type":"user","message":{"content":"<command-name>/reload-plugins</command-name>\n<command-message>reload-plugins</command-message>"}}`,
		`{"type":"user","message":{"content":"<local-command-stdout>Reloaded: 1 plugin</local-command-stdout>"}}`), tailBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got := turnFromTranscript(es); !got.Replied || got.Text != "Done." {
		t.Errorf("a slash command must not open a new, unanswered turn: %+v", got)
	}
}
