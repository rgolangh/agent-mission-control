#!/usr/bin/env bash
# One beat of the promo scene, called by the recorder (promo/shell.qml) at
# its timestamps so the take matches its captions:
#
#   cue.sh sync-0|sync-1|sync-2|sync-done|ask   the fake OpenCode sessions
#   cue.sh claude-start|claude-tool|claude-done the Claude Code session, played
#                                               through the real `amc hook
#                                               claude-code` command
#
# Needs DEMO_STATE, DEMO_FAKE_PORT and XDG_RUNTIME_DIR from demo-env.sh.
set -euo pipefail
state=${DEMO_STATE:?} port=${DEMO_FAKE_PORT:?}

sid=8b3e0d4a-2f6c-4b1e-9a7d-c0ffeeGatewy
cwd=$HOME/src/gateway
transcript=$state/claude/projects/-demo/$sid.jsonl
prompt="Add per-client rate limiting to the gateway"
answer="Added per-client rate limiting (429 + Retry-After) with a token bucket per API key. go test ./gateway/... passes."

hook() {
  local base="\"session_id\":\"$sid\",\"cwd\":\"$cwd\",\"transcript_path\":\"$transcript\""
  echo "{$base,$1}" | "$state/bin/amc" hook claude-code
}

case ${1:-} in
  sync-0 | sync-1 | sync-2 | sync-done | ask)
    curl -fsS -X POST "http://127.0.0.1:$port/demo/cue/$1" ;;
  claude-start)
    mkdir -p "$(dirname "$transcript")"
    cat >"$transcript" <<EOF
{"type":"user","message":{"role":"user","content":"$prompt"}}
{"type":"ai-title","aiTitle":"Rate-limit the API gateway"}
EOF
    hook '"hook_event_name":"SessionStart","source":"startup"'
    hook "\"hook_event_name\":\"UserPromptSubmit\",\"prompt\":\"$prompt\""
    hook '"hook_event_name":"PreToolUse","tool_name":"Read","tool_use_id":"toolu_01","tool_input":{"file_path":"gateway/limit.go"}' ;;
  claude-tool)
    hook '"hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"toolu_02","tool_input":{"command":"go test ./gateway/..."}' ;;
  claude-done)
    hook '"hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"toolu_02"'
    echo "{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"stop_reason\":\"end_turn\",\"content\":[{\"type\":\"text\",\"text\":\"$answer\"}]}}" >>"$transcript"
    hook "\"hook_event_name\":\"Stop\",\"last_assistant_message\":\"$answer\"" ;;
  *)
    echo "usage: $0 sync-0|sync-1|sync-2|sync-done|ask|claude-start|claude-tool|claude-done" >&2
    exit 2 ;;
esac
