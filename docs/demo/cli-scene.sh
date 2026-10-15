#!/usr/bin/env bash
# The terminal half of the README demo, played inside `asciinema rec` by
# record-cli.sh against the demo-env.sh daemon. It "types" each command, then runs it.
set -u

type_cmd() {
  printf '\033[1;32m$\033[0m '
  local s=$1 i
  for ((i = 0; i < ${#s}; i++)); do
    printf '%s' "${s:i:1}"
    sleep 0.035
  done
  sleep 0.4
  printf '\n'
}

run() {
  type_cmd "$*"
  eval "$*"
  sleep "${PAUSE:-2.5}"
}

note() {
  printf '\033[2m# %s\033[0m\n' "$1"
  sleep 1.2
}

# Wait for the opening of the story (two agents waiting on you) before rolling.
until [ "$(mission-control status 2>/dev/null | grep -c "^needs_input")" -ge 2 ]; do sleep 0.2; done
clear
sleep 0.5

note "five agent sessions, two tools: who needs me?"
PAUSE=4.5 run mission-control status
clear
note "an agent asked a question; see it without finding its tab"
PAUSE=3.5 run mission-control show 9kAuthMw
run mission-control answer 9kAuthMw Redis
run mission-control answer DepsBmp once
note "the stuck one only needed Docker; nudge it"
run mission-control send SyncTst \"Docker is running now, try again\"
note "a moment later…"
sleep 3
clear
PAUSE=5 run mission-control status
