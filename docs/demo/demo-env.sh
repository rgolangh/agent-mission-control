#!/usr/bin/env bash
# Start a throwaway agent-mission-control setup that plays the README demo story:
# a scripted fake OpenCode server (fake-opencode/), a daemon watching it, and
# one Claude Code session fed through the real hook command. Nothing touches
# your real daemon, sessions or ~/.claude.
#
#   docs/demo/demo-env.sh start     # prints the env to export
#   docs/demo/demo-env.sh stop
#   docs/demo/demo-env.sh build     # just the binaries (start does it too)
#
# DEMO_LIVE=1 runs the demo daemon in your real runtime dir and on the
# widget's port (127.0.0.1:4200), so the installed omarchy-shell widget shows
# the demo. Stop your own daemon first (systemctl --user stop agent-mission-control)
# and start it again afterwards.
set -euo pipefail

repo=$(cd "$(dirname "$0")/../.." && pwd)
state=${DEMO_STATE:-/tmp/mc-demo}   # short: it holds a unix socket path
fake_port=${DEMO_FAKE_PORT:-4396}

stop() {
  for f in "$state"/*.pid; do
    [ -e "$f" ] && kill "$(cat "$f")" 2>/dev/null || true
  done
  [ -d "$state" ] && find "$state" -mindepth 1 -maxdepth 1 ! -name bin -exec rm -rf {} +
  return 0
}

build() {
  mkdir -p "$state/bin"
  (cd "$repo" && go build -o "$state/bin/" ./cmd/amc ./docs/demo/fake-opencode)
}

start() {
  stop
  [ "${DEMO_SKIP_BUILD:-}" = 1 ] || build
  mkdir -p "$state/claude/projects/-demo"
  chmod 700 "$state"

  if [ "${DEMO_LIVE:-}" = 1 ]; then
    runtime=$XDG_RUNTIME_DIR listen=127.0.0.1:4200
    if "$state/bin/amc" status >/dev/null 2>&1; then
      echo "an agent-mission-control daemon is already running; stop it first" >&2
      exit 1
    fi
  else
    runtime=$state/run listen=127.0.0.1:4299
    mkdir -p "$runtime" && chmod 700 "$runtime"
  fi

  "$state/bin/fake-opencode" -listen "127.0.0.1:$fake_port" >"$state/fake.log" 2>&1 &
  echo $! >"$state/fake.pid"
  XDG_RUNTIME_DIR=$runtime CLAUDE_CONFIG_DIR=$state/claude \
    "$state/bin/amc" serve -listen "$listen" -opencode "http://127.0.0.1:$fake_port" \
    >"$state/daemon.log" 2>&1 &
  echo $! >"$state/daemon.pid"
  for _ in $(seq 50); do
    XDG_RUNTIME_DIR=$runtime "$state/bin/amc" status >/dev/null 2>&1 && break
    sleep 0.1
  done

  claude_session "$runtime"

  cat <<EOF
export XDG_RUNTIME_DIR=$runtime
export PATH=$state/bin:\$PATH
EOF
}

# claude_session plays a finished Claude Code turn through the hook command,
# with a transcript in the demo's own CLAUDE_CONFIG_DIR for the title and the
# done/stuck judgment.
claude_session() {
  local sid=4f2c9b1e-6d0a-4c3e-9f1b-a1b2RateLim
  local cwd=$HOME/src/gateway
  local transcript=$state/claude/projects/-demo/$sid.jsonl
  local answer="Added per-key rate limiting (429 + Retry-After). Tests pass."
  cat >"$transcript" <<EOF
{"type":"user","message":{"role":"user","content":"Add per-client rate limiting to the gateway"}}
{"type":"ai-title","aiTitle":"Rate-limit the API gateway"}
{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"$answer"}]}}
EOF
  local base="\"session_id\":\"$sid\",\"cwd\":\"$cwd\",\"transcript_path\":\"$transcript\""
  for ev in \
    "{$base,\"hook_event_name\":\"SessionStart\",\"source\":\"startup\"}" \
    "{$base,\"hook_event_name\":\"UserPromptSubmit\",\"prompt\":\"Add per-client rate limiting to the gateway\"}" \
    "{$base,\"hook_event_name\":\"Stop\",\"last_assistant_message\":\"$answer\"}"; do
    echo "$ev" | XDG_RUNTIME_DIR=$1 "$state/bin/amc" hook claude-code
  done
}

case ${1:-} in
  start) start ;;
  stop) stop ;;
  build) build ;;
  *) echo "usage: $0 start|stop|build" >&2; exit 2 ;;
esac
