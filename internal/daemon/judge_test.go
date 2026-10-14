package daemon

import (
	"testing"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

func TestJudge(t *testing.T) {
	reply := func(text string) adapter.TurnEnd {
		return adapter.TurnEnd{Text: text, Finish: "stop", Replied: true}
	}
	cases := []struct {
		name   string
		turn   adapter.TurnEnd
		status adapter.Status
		rule   string
	}{
		// Hard facts first.
		{"interrupted", adapter.TurnEnd{Error: adapter.TurnErrorAborted, Replied: true, Text: "All done."}, adapter.StatusStuck, "aborted"},
		{"context overflow", adapter.TurnEnd{Error: adapter.TurnErrorContextOverflow, Replied: true}, adapter.StatusStuck, "context-overflow"},
		{"output length error", adapter.TurnEnd{Error: adapter.TurnErrorOutputLength, Replied: true}, adapter.StatusStuck, "output-limit"},
		{"finish=length", adapter.TurnEnd{Finish: "length", Replied: true, Text: "Here is the first part of"}, adapter.StatusStuck, "output-limit"},
		{"content filter", adapter.TurnEnd{Finish: "content-filter", Replied: true}, adapter.StatusStuck, "content-filter"},
		{"auth", adapter.TurnEnd{Error: adapter.TurnErrorAuth, ErrorMessage: "invalid x-api-key"}, adapter.StatusFailed, "auth-error"},
		{"api error", adapter.TurnEnd{Error: adapter.TurnErrorAPI, ErrorMessage: "overloaded"}, adapter.StatusFailed, "error"},
		{"no reply", adapter.TurnEnd{}, adapter.StatusStuck, "no-reply"},
		{"tool failed last", adapter.TurnEnd{Replied: true, Finish: "tool-calls", LastToolFailed: "bash"}, adapter.StatusStuck, "tool-failed"},
		{"empty", adapter.TurnEnd{Replied: true, Finish: "stop", Text: "  \n"}, adapter.StatusStuck, "no-final-message"},
		{"only reasoning", reply("<think>let me see</think>"), adapter.StatusStuck, "no-final-message"},
		{"mid step", adapter.TurnEnd{Replied: true, Finish: "tool-calls", Text: "Now let me run the tests."}, adapter.StatusStuck, "mid-step"},

		// Questions vs offers vs greetings.
		{"greeting (real)", reply("Hi! What can I help you with?"), adapter.StatusDone, "greeting"},
		{"anything else", reply("Done. Anything else?"), adapter.StatusDone, "greeting"},
		{"choice question", reply("I found two session stores.\n\nShould I use Redis or Postgres?"), adapter.StatusNeedsInput, "question"},
		{"question then options", reply("Two ways to fix this. Which do you prefer?\n\n1. Patch the parser\n2. Rewrite it"), adapter.StatusNeedsInput, "question"},
		{"colon then options", reply("There are two approaches, pick one:\n- **A**: keep the cache\n- **B**: drop it"), adapter.StatusNeedsInput, "request"},
		{"permission to proceed", reply("The migration will drop the users table. Would you like me to proceed?"), adapter.StatusNeedsInput, "question"},
		{"bold question", reply("**Should I delete the old config files?**"), adapter.StatusNeedsInput, "question"},
		{"offer", reply("Implemented the retry logic and added tests; all pass.\n\nWant me to also update the README?"), adapter.StatusDone, "offer"},
		{"offer would you like", reply("Fixed the bug in auth.go.\n\nWould you like me to add a regression test?"), adapter.StatusDone, "offer"},
		{"let me know if", reply("Refactor complete. Let me know if you want any changes."), adapter.StatusDone, "finished"},
		{"question in code ignored", reply("Added the helper:\n```go\nx := a ? b : c // why?\n```\nDone."), adapter.StatusDone, "finished"},
		{"request confirm", reply("I drafted both options above. Please confirm which one you want me to apply."), adapter.StatusNeedsInput, "request"},
		{"request let me know which", reply("Let me know which environment to deploy to."), adapter.StatusNeedsInput, "request"},

		// Blockers.
		{"subagent could not (real)", reply("The subagent reported it could not complete the task: it has no `question` tool available to ask the user."), adapter.StatusStuck, "blocker"},
		{"I couldn't", reply("I updated the handler, but I couldn't run the integration tests because Docker isn't running."), adapter.StatusStuck, "blocker"},
		{"tests still fail", reply("Applied the fix. The tests still fail with a timeout in TestSync."), adapter.StatusStuck, "blocker"},
		{"not sure", reply("I looked at the logs. I'm not sure why the worker exits."), adapter.StatusStuck, "blocker"},
		{"permission denied", reply("Tried to write /etc/hosts: permission denied."), adapter.StatusStuck, "blocker"},
		{"blocker then question wins", reply("I couldn't find the config. Where does it live?"), adapter.StatusNeedsInput, "question"},
		{"blocker beats offer", reply("Fixed the parser. I couldn't run the e2e suite (no DB). Want me to add a mock DB?"), adapter.StatusStuck, "blocker"},
		{"resolved blocker", reply("At first I couldn't reproduce it, but after pinning the version it's fixed and all tests pass."), adapter.StatusDone, "finished"},
		{"tests pass now", reply("Tests failed before the change; now all tests pass."), adapter.StatusDone, "finished"},

		// Todos.
		{"open todos", adapter.TurnEnd{Replied: true, Finish: "stop", Text: "Finished the parser part.", TodosTotal: 5, TodosOpen: 2}, adapter.StatusStuck, "open-todos"},
		{"todos done", adapter.TurnEnd{Replied: true, Finish: "stop", Text: "All done.", TodosTotal: 5}, adapter.StatusDone, "finished"},
		{"question beats todos", adapter.TurnEnd{Replied: true, Finish: "stop", Text: "Which port should it use?", TodosTotal: 3, TodosOpen: 3}, adapter.StatusNeedsInput, "question"},

		// Plain answers (real).
		{"one word", reply("Postgres"), adapter.StatusDone, "finished"},
		{"think tag residue", reply("</think>OK"), adapter.StatusDone, "finished"},
		{"summary", reply("The directory is empty.\n\n- **Total files:** 0"), adapter.StatusDone, "finished"},
	}
	for _, c := range cases {
		v := Judge(c.turn)
		if v.Status != c.status || v.Rule != c.rule {
			t.Errorf("%s: got %s/%s (%q), want %s/%s", c.name, v.Status, v.Rule, v.Message, c.status, c.rule)
		}
	}
}

// Real closing text of turns this project's own development session
// misjudged (2026-10-14 replay with `mission-control judge -all`), trimmed.
// Long, structured messages (headline, lists, questions inside list items)
// are how capable agents write, and what the first rules missed.
func TestJudgeRealStructuredMessages(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		status adapter.Status
		rule   string
	}{
		{"decisions as a numbered list of questions", `Packaging has five pieces; most port trivially.

A few questions shape what I'd build:

1. **What desktop does the Fedora machine run?** GNOME, KDE, or a tiling window manager. This decides the widget question.
2. **Which agents do you use there?** Claude Code, OpenCode, or both.
3. **Is the code going on GitHub?** The repo is local-only right now.`, adapter.StatusNeedsInput, "question"},
		{"list intro says what it needs from you", "`install` could also run the plugin steps for you.\n\nTo build this I need:\n- Your GitHub username or org, for the module path.\n- The output of `gnome-shell --version` on the Fedora machine.\n- Whether I should go ahead and build it all.", adapter.StatusNeedsInput, "request"},
		{"two things need your call", "I didn't work on the diagram package. Two things need your call:\n\n1. **Install the widget into your live bar?** That changes your `shell.json`.\n2. **Done vs. stuck:** may the daemon call an LLM? If not, I can build better rules.", adapter.StatusNeedsInput, "question"},
		{"waits for a confirmation", "4. **`CLAUDE.md`.** Keep it public as-is, trim it, or drop it.\n\nAfter that, creating the repo is one command. I'll run the create step once you confirm the name.", adapter.StatusNeedsInput, "request"},
		{"blocked headline, then waits", "The repo is ready to push, but I couldn't push it: your token can't see the repo, so GitHub answers \"not found\".\n\n**Done while waiting:**\n- **Module path:** renamed.\n- **License:** Apache-2.0.\n- **Author:** rewritten.\n- **Remote:** set.\n\nOnce access works, tell me and I'll check the repo is private and empty, then push. Or run it yourself with `git push -u origin main`.", adapter.StatusNeedsInput, "request"},
		{"blocked headline only", "I couldn't push the branch: the remote rejected the token.\n\nEverything else is committed:\n- Tests pass.\n- Docs updated.\n- Release workflow added.\n- CI pinned.\n- Changelog written.\n- Version bumped.\n- Tag prepared.", adapter.StatusStuck, "blocker"},
		{"limitation that turned out fine", "- **Your other session** shows as **needs_input: waiting on a permission prompt**. That was one of the interactive cases I couldn't test headlessly, and it works.\n\nTwo small things to know:\n- **Plain opencode and --port both work.**\n- **If an upgrade breaks the bridge**, it prints a warning.", adapter.StatusDone, "finished"},
		{"plain summary list stays done", "All six findings are fixed and verified live.\n\n- H1: bridge requires a token.\n- M1: hook payloads minimized.\n- M4: plain text everywhere.", adapter.StatusDone, "finished"},
		{"rhetorical question list item is still a question", "Here is the plan:\n\n1. Ship the fix.\n2. Should we also backport it?", adapter.StatusNeedsInput, "question"},
	}
	for _, c := range cases {
		v := Judge(adapter.TurnEnd{Text: c.text, Finish: "stop", Replied: true})
		if v.Status != c.status || v.Rule != c.rule {
			t.Errorf("%s: got %s/%s (%q), want %s/%s", c.name, v.Status, v.Rule, v.Message, c.status, c.rule)
		}
	}
}

func TestJudgeMessagesCarryEvidence(t *testing.T) {
	v := Judge(adapter.TurnEnd{Replied: true, Finish: "stop", Text: "Looked into it.\n\nShould I use **Redis** or Postgres?"})
	if v.Message != "asks: Should I use Redis or Postgres?" {
		t.Errorf("question message = %q", v.Message)
	}
	v = Judge(adapter.TurnEnd{Replied: true, Finish: "stop", Text: "## Summary\nRenamed the package.\nAll good."})
	if v.Message != "finished: Renamed the package." {
		t.Errorf("finished message = %q", v.Message)
	}
	// Seen live: the offer is the whole message, so don't quote it twice.
	v = Judge(adapter.TurnEnd{Replied: true, Finish: "stop", Text: "The refactor is finished—would you like me to also add unit tests?"})
	if v.Message != "finished; offers: The refactor is finished—would you like me to also add unit tests?" {
		t.Errorf("one-line offer message = %q", v.Message)
	}
	v = Judge(adapter.TurnEnd{Error: adapter.TurnErrorAPI, ErrorMessage: "529 overloaded"})
	if v.Message != "error: 529 overloaded" {
		t.Errorf("error message = %q", v.Message)
	}
}

func TestHungRunningWorkflowIsFlaggedAndRecovers(t *testing.T) {
	s := NewStore()
	t0 := time.Now()
	s.Apply(ev("busy", adapter.StatusRunning, "working", t0))
	s.Apply(askEvent("asking", question("q1", false, true), t0))
	s.Apply(ev("finished", adapter.StatusDone, "finished: ok", t0))
	s.Apply(ev("streaming", adapter.StatusRunning, "working", t0))
	s.Touch("opencode:streaming", t0.Add(14*time.Minute)) // output kept flowing

	got := s.FlagHung(t0.Add(16*time.Minute), 15*time.Minute)
	if len(got) != 1 || got[0] != "opencode:busy" {
		t.Fatalf("flagged %v, want only the silent running workflow", got)
	}
	if w, _ := s.Get("opencode:busy"); w.Status != adapter.StatusStuck || w.Message != "no activity for 16m0s: hung, or a long silent command" {
		t.Fatalf("busy = %s %q", w.Status, w.Message)
	}
	if again := s.FlagHung(t0.Add(20*time.Minute), 15*time.Minute); len(again) != 0 {
		t.Errorf("already-stuck workflow flagged again: %v", again)
	}

	// Any sign of life undoes it; a judged stuck (not hung) is left alone.
	if !s.Touch("opencode:busy", t0.Add(21*time.Minute)) {
		t.Fatal("touch should revive a hung workflow")
	}
	if w, _ := s.Get("opencode:busy"); w.Status != adapter.StatusRunning {
		t.Errorf("after touch: %s", w.Status)
	}
	s.Apply(ev("judged", adapter.StatusStuck, "blocked: x", t0))
	if s.Touch("opencode:judged", t0.Add(time.Minute)) {
		t.Error("touch must not undo a stuck verdict from Judge")
	}
}
