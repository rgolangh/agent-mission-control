#!/usr/bin/env bash
# Record the omarchy-shell widget half of the README demo, on your real bar.
#
#   docs/demo/record-omarchy.sh [out-dir]
#
# What happens:
#   1. builds the demo binaries and checks it can capture (nothing changed yet)
#   2. stops your mission-control service and runs the demo daemon in its place
#      (demo-env.sh, DEMO_LIVE=1), so the installed widget shows the story
#   3. switches to an empty workspace and captures the top-right corner of the
#      focused monitor while a script plays the take: notifications, then the
#      panel opens (omarchy-shell mission-control open) and the CLI answers
#      the question, approves the permission and nudges the stuck agent, so
#      the panel updates live. Hands off: nothing to click.
#   4. switches back, starts your service again (also on Ctrl-C or failure)
#      and renders omarchy.gif (+ omarchy.mp4)
#
# The installed widget must have the IPC handler (ui/omarchy/Widget.qml);
# after updating it run `omarchy restart shell`.
#
# DEMO_REGION="X,Y WxH" overrides the corner (DEMO_CORNER=WxH sets its size,
# default 720x720). Capture is a grim loop rather than a screen recorder: it
# needs no GPU encoder (gpu-screen-recorder fails on virtual GPUs) and grim
# ships with Omarchy. Needs go, grim, hyprctl, jq, ffmpeg.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${1:-$here}
frames=$(mktemp -d)

say() { printf '\033[1m==> %s\033[0m\n' "$*"; }

say "building the demo binaries"
"$here/demo-env.sh" build

if [ -z "${DEMO_REGION:-}" ]; then
  corner=${DEMO_CORNER:-720x720}
  cw=${corner%x*} ch=${corner#*x}
  DEMO_REGION=$(hyprctl monitors -j | jq -r --argjson cw "$cw" --argjson ch "$ch" '
    .[] | select(.focused) |
    (.width / .scale | floor) as $w |
    "\(.x + $w - $cw),\(.y) \($cw)x\($ch)"')
fi
region=$DEMO_REGION
grim -g "$region" "$frames/probe.png" # fail now, before anything is changed
rm "$frames/probe.png"

omarchy-shell mission-control close || {
  echo "the installed widget has no IPC handler: update it and run 'omarchy restart shell'" >&2
  exit 1
}
read -r -p "Ready to record $region (about 35s, you are moved to an empty workspace). Press Enter… "

was_active=0
systemctl --user is-active --quiet mission-control && was_active=1
prev_ws=$(hyprctl activeworkspace -j | jq -r .id)
cap=
# workspace switches with Hyprland's Lua dispatch (0.56), else the old form.
workspace() {
  hyprctl dispatch "hl.dsp.focus({ workspace = \"$1\" })" >/dev/null 2>&1 ||
    hyprctl dispatch workspace "$1" >/dev/null
}
restore() {
  if [ -n "$cap" ]; then kill "$cap" 2>/dev/null || true; wait "$cap" 2>/dev/null || true; fi
  cap=
  "$here/demo-env.sh" stop
  if [ "$was_active" = 1 ]; then systemctl --user start mission-control; fi
  was_active=0
  workspace "$prev_ws"
}
trap restore EXIT
trap 'exit 130' INT TERM

# capture grabs frames back to back and notes when each was taken, so
# playback keeps real time whatever the frame rate.
capture() {
  local i=0
  while :; do
    i=$((i + 1))
    date +%s.%N >>"$frames/times"
    grim -c -l 1 -g "$region" "$frames/$(printf %06d $i).png"
  done
}

say "stopping your mission-control service (restarted afterwards)"
if [ "$was_active" = 1 ]; then systemctl --user stop mission-control; fi
workspace empty
sleep 0.5
capture &
cap=$!
sleep 2 # a beat of the quiet bar before the agents show up
DEMO_LIVE=1 DEMO_SKIP_BUILD=1 "$here/demo-env.sh" start >/dev/null
mc() { "${DEMO_STATE:-/tmp/mc-demo}/bin/mission-control" "$@" >/dev/null; }
sleep 5 # the agents settle; the bar count and notifications appear
omarchy-shell mission-control open
sleep 4
mc answer 9kAuthMw Redis
sleep 4
mc answer DepsBmp once
sleep 4
mc send SyncTst "Docker is running now, try again"
sleep 9 # all three finish
omarchy-shell mission-control close
sleep 2
restore
trap - EXIT

n=$(wc -l <"$frames/times")
say "rendering $n frames"
# Each frame lasts until the next one was taken.
awk -v dir="$frames" '
  { t[NR] = $1 }
  END {
    for (i = 1; i <= NR; i++) {
      d = (i < NR) ? t[i+1] - t[i] : 2
      printf "file '\''%s/%06d.png'\''\nduration %.3f\n", dir, i, d
    }
    printf "file '\''%s/%06d.png'\''\n", dir, NR
  }' "$frames/times" >"$frames/list.txt"

ffmpeg -y -loglevel error -f concat -safe 0 -i "$frames/list.txt" \
  -vf "fps=30,scale='min(1280,iw)':-2" -c:v libx264 -crf 20 -pix_fmt yuv420p -movflags +faststart \
  "$out/omarchy.mp4"
ffmpeg -y -loglevel error -i "$out/omarchy.mp4" -vf \
  "fps=12,scale='min(960,iw)':-1:flags=lanczos,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=4" \
  "$out/omarchy.gif"
rm -rf "$frames"
ls -l "$out"/omarchy.*
