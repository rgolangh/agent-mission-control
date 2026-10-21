# README demo

The clips in the top-level README are recorded from a scripted story, not
from real agents, so every take is the same, costs no tokens and shows
nothing private:

- `fake-opencode/`: a stand-in for `opencode serve` that speaks the parts of
  OpenCode's HTTP/SSE API agent-mission-control uses. Four sessions settle into
  running, stuck, waiting on a question and waiting on a permission, then
  react to answers and follow-ups like a real agent would.
- `demo-env.sh start|stop`: runs it with an agent-mission-control daemon in a
  throwaway runtime dir (`/tmp/mc-demo`, port 4299), plus one finished
  Claude Code session played through the real `amc hook
  claude-code`. Your own daemon and sessions are untouched.

## Re-recording

```bash
docs/demo/record-cli.sh       # -> cli.gif (+ cli.mp4); asciinema 3, agg, ffmpeg
docs/demo/record-omarchy.sh   # -> omarchy.gif (+ omarchy.mp4), no longer in the README; grim, jq, ffmpeg
```

`record-cli.sh` is unattended: it plays `cli-scene.sh` in a headless
asciinema session.

`record-omarchy.sh` records your real bar, hands off. After one Enter it
stops the `agent-mission-control` user service and runs the demo daemon in its place
(`DEMO_LIVE=1`: your runtime dir, port 4200), so the installed widget shows the
story. It moves you to an empty workspace and captures the top-right corner
with grim while a script opens the panel (`omarchy-shell agent-mission-control
open`) and answers, approves and nudges through the CLI. Then it switches back
and starts your service again, also on Ctrl-C or failure. The installed widget
must have the IPC handler: after updating `ui/omarchy`, run `omarchy restart
shell`. `DEMO_CORNER=WxH` (default 720x720) or `DEMO_REGION="X,Y WxH"` move
the frame.

The GIFs are committed. The MP4s are not (`.gitignore`): drag one into a
GitHub comment or release editor if you want an inline player.

## Promo clip

```bash
docs/demo/record-promo.sh     # -> promo.mp4 (1920x1080, for X) + promo.gif; go, qs, curl, ffmpeg
```

A 29s take for sharing (shot list and post drafts in `promo.md`): three
agents work, one asks a question, the bar widget turns urgent and notifies,
the question is answered from the panel, everyone finishes. Unlike
`record-omarchy.sh` it never touches your screen or your service:
`promo/shell.qml` runs in an offscreen Quickshell, draws a mock desktop around
the real widget and writes frames itself, against a throwaway daemon
(`/tmp/mc-promo`, port 4298) and `fake-opencode -story promo`, whose beats
`promo/cue.sh` triggers on the scene's timeline. notify-send is stubbed, so
the toast is drawn from what the widget sent. If a Claude Code process is an
ancestor of the recorder (running it from inside Claude Code), the Claude card
gets a terminal button.

To poke at the story by hand:

```bash
eval "$(docs/demo/demo-env.sh start)"
amc status
docs/demo/demo-env.sh stop
```
