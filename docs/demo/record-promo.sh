#!/usr/bin/env bash
# Record the promo clip (about 27s): three agents at work, one asks a
# question, the Omarchy bar widget turns urgent and notifies, the question is
# answered from the panel, everything finishes, the bar calms down.
#
#   docs/demo/record-promo.sh [out-dir]    # -> promo.mp4 (1920x1080, for X) + promo.gif
#
# Fully isolated, safe to run on the desktop you are using: Quickshell runs
# offscreen (QT_QPA_PLATFORM=offscreen, no Wayland or Hyprland connection),
# the daemon is a throwaway one (/tmp/mc-promo, port 4298, its own runtime
# dir), notify-send is a stub that only writes a file, and the D-Bus session
# address points nowhere. Nothing shows on screen, your agent-mission-control
# service and bar are not touched.
#
# The scene (promo/shell.qml) loads the real widget from ui/omarchy and
# drives the story through promo/cue.sh. Needs go, qs (quickshell), curl,
# ffmpeg and the JetBrainsMono Nerd Font.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
out=${1:-$here}

export DEMO_STORY=promo DEMO_STATE=${DEMO_STATE:-/tmp/mc-promo}
export DEMO_FAKE_PORT=${DEMO_FAKE_PORT:-4397} DEMO_LISTEN=${DEMO_LISTEN:-127.0.0.1:4298}

work=$(mktemp -d)
cleanup() { "$here/demo-env.sh" stop; if [ -n "${PROMO_KEEP:-}" ]; then echo "kept $work"; else rm -rf "$work"; fi; }
trap cleanup EXIT

mkdir -p "$work/frames" "$work/stub"
: >"$work/notify"
cat >"$work/stub/notify-send" <<EOF
#!/bin/sh
# The widget's notification, for the scene to draw: summary, then body.
for a; do summary=\$body body=\$a; done
printf '%s\n%s\n' "\$summary" "\$body" >"$work/notify"
EOF
chmod +x "$work/stub/notify-send"

eval "$("$here/demo-env.sh" start)" # exports XDG_RUNTIME_DIR and PATH

echo "==> recording (offscreen, about 30s)"
(
  unset WAYLAND_DISPLAY DISPLAY HYPRLAND_INSTANCE_SIGNATURE QT_QPA_PLATFORMTHEME
  export DBUS_SESSION_BUS_ADDRESS=unix:path=/nonexistent
  export QT_QPA_PLATFORM=offscreen QT_SCALE_FACTOR=1.5 # 1280x720 scene -> 1920x1080 frames
  export PATH=$work/stub:$PATH
  export MC_URL=http://$DEMO_LISTEN MC_WIDGET_DIR=$repo/ui/omarchy
  export PROMO_FRAMES=$work/frames PROMO_CUE=$here/promo/cue.sh PROMO_NOTIFY=$work/notify
  timeout 90 qs -p "$here/promo" >"$work/qs.log" 2>&1 || true
)
if grep -qE "TIMEOUT|LOAD FAILED|no chip" "$work/qs.log" || ! ls "$work/frames" | grep -q .; then
  cat "$work/qs.log" >&2
  exit 1
fi

# Frames are named f<index>-<ms since start>.png; each lasts until the next.
(cd "$work/frames" && ls f*.png) | awk -F'[-.]' -v dir="$work/frames" '
  { f[NR] = $0; t[NR] = $2 / 1000 }
  END {
    for (i = 1; i <= NR; i++) {
      d = (i < NR) ? t[i+1] - t[i] : 1
      printf "file '\''%s/%s'\''\nduration %.3f\n", dir, f[i], d
    }
    printf "file '\''%s/%s'\''\n", dir, f[NR]
  }' >"$work/list.txt"
echo "==> rendering $(grep -c '^duration' "$work/list.txt") frames"

ffmpeg -y -loglevel error -f concat -safe 0 -i "$work/list.txt" \
  -vf "fps=30,format=yuv420p" -c:v libx264 -preset slow -crf 18 -movflags +faststart \
  "$out/promo.mp4"
ffmpeg -y -loglevel error -i "$out/promo.mp4" -vf \
  "fps=12,scale=960:-1:flags=lanczos,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle" \
  "$out/promo.gif"
ls -l "$out"/promo.*
