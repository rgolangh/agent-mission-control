# README demo

The clips in the top-level README are recorded from a scripted story, not
from real agents, so every take is the same, costs no tokens and shows
nothing private:

- `fake-opencode/`: a stand-in for `opencode serve` that speaks the parts of
  OpenCode's HTTP/SSE API mission-control uses. Four sessions settle into
  running, stuck, waiting on a question and waiting on a permission, then
  react to answers and follow-ups like a real agent would.
- `demo-env.sh start|stop`: runs it with a mission-control daemon in a
  throwaway runtime dir (`/tmp/mc-demo`, port 4299), plus one finished
  Claude Code session played through the real `mission-control hook
  claude-code`. Your own daemon and sessions are untouched.

## Re-recording

```bash
docs/demo/record-cli.sh       # -> cli.gif (+ cli.mp4); asciinema 3, agg, ffmpeg
docs/demo/record-omarchy.sh   # -> omarchy.gif (+ omarchy.mp4); grim, jq, ffmpeg
```

`record-cli.sh` is unattended: it plays `cli-scene.sh` in a headless
asciinema session.

`record-omarchy.sh` records your real bar, hands off. After one Enter it
stops the `mission-control` user service and runs the demo daemon in its place
(`DEMO_LIVE=1`: your runtime dir, port 4200), so the installed widget shows the
story. It moves you to an empty workspace and captures the top-right corner
with grim while a script opens the panel (`omarchy-shell mission-control
open`) and answers, approves and nudges through the CLI. Then it switches back
and starts your service again, also on Ctrl-C or failure. The installed widget
must have the IPC handler: after updating `ui/omarchy`, run `omarchy restart
shell`. `DEMO_CORNER=WxH` (default 720x720) or `DEMO_REGION="X,Y WxH"` move
the frame.

The GIFs are committed. The MP4s are not (`.gitignore`): drag one into a
GitHub comment or release editor if you want an inline player.

To poke at the story by hand:

```bash
eval "$(docs/demo/demo-env.sh start)"
mission-control status
docs/demo/demo-env.sh stop
```
