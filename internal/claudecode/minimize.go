package claudecode

import "unicode/utf8"

// Limits for forwarded text. The adapter needs a prompt's first line (a
// title), the end of the final message (closing questions, blockers) and its
// first line (the summary).
const (
	maxPromptBytes = 300
	maxFieldBytes  = 300
	maxFinalHead   = 2 << 10
	maxFinalTail   = 8 << 10
)

// summaryKeys are the tool_input fields toolSummary reads.
var summaryKeys = []string{"command", "file_path", "url", "pattern", "path"}

// Minimize reduces a raw hook payload to the fields Adapter.Normalize
// reads, before it leaves the hook process. Claude Code hands hooks
// everything: full prompts, tool inputs (file contents for Write/Edit) and
// tool_response (file contents, command output). None of that is needed to
// track state, and whatever is forwarded is exposed to the daemon's API.
// So drop it at the source.
func Minimize(in map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{
		"session_id", "transcript_path", "cwd", "hook_event_name",
		"source", "reason", "tool_name", "tool_use_id", "notification_type",
		"mc_agent_pid",
	} {
		if v, ok := in[k]; ok {
			out[k] = v
		}
	}
	if s, ok := in["prompt"].(string); ok {
		out["prompt"] = head(s, maxPromptBytes)
	}
	if s, ok := in["message"].(string); ok {
		out["message"] = head(s, maxFieldBytes)
	}
	if s, ok := in["last_assistant_message"].(string); ok {
		if len(s) > maxFinalHead+maxFinalTail {
			s = head(s, maxFinalHead) + "\n…\n" + tail(s, maxFinalTail)
		}
		out["last_assistant_message"] = s
	}
	if ti, ok := in["tool_input"].(map[string]any); ok {
		if in["tool_name"] == "AskUserQuestion" {
			out["tool_input"] = ti // the question and its options are the point
		} else {
			small := map[string]any{}
			for _, k := range summaryKeys {
				if s, ok := ti[k].(string); ok {
					small[k] = head(s, maxFieldBytes)
				}
			}
			out["tool_input"] = small
		}
	}
	return out
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}
