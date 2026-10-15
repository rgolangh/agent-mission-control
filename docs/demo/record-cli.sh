#!/usr/bin/env bash
# Record the CLI half of the README demo: starts demo-env.sh, plays
# cli-scene.sh under asciinema and renders docs/demo/cli.gif (+ cli.mp4).
# Needs go, asciinema 3, agg and ffmpeg.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${1:-$here}
cols=${DEMO_COLS:-150} rows=${DEMO_ROWS:-24}

eval "$("$here/demo-env.sh" start)"
trap '"$here/demo-env.sh" stop' EXIT

asciinema rec --overwrite --headless --window-size "${cols}x${rows}" \
  --command "$here/cli-scene.sh" "$out/cli.cast"
agg --quiet --theme github-dark --font-size 18 --idle-time-limit 6 --last-frame-duration 4 \
  "$out/cli.cast" "$out/cli.gif"
ffmpeg -y -loglevel error -i "$out/cli.gif" -movflags +faststart -pix_fmt yuv420p \
  -vf "fps=30,scale=trunc(iw/2)*2:trunc(ih/2)*2" "$out/cli.mp4"
ls -l "$out"/cli.*
