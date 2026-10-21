# Agent Mission Control: omarchy-shell bar widget

A bar icon that counts the agent workflows that need you (waiting on an
answer, stuck or failed; in the theme's urgent color, pulsing while one is
waiting on an answer, with an orbiting spinner while agents are running), and
a panel listing every workflow, most urgent first,
where you can answer questions and approve permissions with one click, or send
a finished or stuck agent a follow-up. Running cards show a three-dot Larson
scanner. A desktop notification fires when an agent starts needing you.

It is a client of the agent-mission-control daemon's HTTP API, so the daemon must be
running (`amc serve -discover`). The daemon's TCP port requires a
bearer token, which the widget reads from
`$XDG_RUNTIME_DIR/agent-mission-control/token` (regenerated on each daemon start;
the widget notices). Agent text is always rendered as plain text, never HTML. It polls `GET /workflows` (every
2s; every 1s while the panel is open). Answering never changes what the widget
shows by itself: the next poll shows what the agent actually did.

## Install

Install the daemon first ([agent-mission-control](https://github.com/rgolangh/agent-mission-control):
the AUR package `agent-mission-control-bin`, or the release binary). Then
either:

- **`amc install`** (recommended): writes this widget from the files embedded
  in the `amc` binary, so it always matches the daemon it talks to. After a
  package upgrade, `amc install` again updates it.
- **`omarchy plugin add https://github.com/rgolangh/omarchy-agent-mission-control --enable`**:
  clones this directory's mirror (released with every daemon release) and
  updates with `omarchy plugin update`. `amc install` sees the git clone and
  leaves it to Omarchy. Use one route or the other: both use the directory
  `~/.config/omarchy/plugins/agent-mission-control`. To switch from
  `amc install` to `plugin add`, run `amc uninstall` (or remove that directory) first.

## Install for development (symlink this directory)

```bash
ln -s "$PWD/ui/omarchy" ~/.config/omarchy/plugins/agent-mission-control
omarchy-shell shell rescanPlugins
omarchy plugin enable agent-mission-control
omarchy bar move   # place it in a bar section
```

Settings (in `~/.config/omarchy/shell.json`, or through the bar's widget
settings): `apiUrl` (default `http://127.0.0.1:4200`), `pollIntervalMs`,
`notify`, `hideWhenDown`.

Mouse: left click toggles the panel, right click refreshes. Esc closes it.

IPC: `omarchy-shell agent-mission-control open|close|toggle`, for a keybind or a
script (the README demo uses it).

## Constraints that shaped it

- **Self-contained.** omarchy-shell loads third-party entry points by
  absolute `file://` URL from outside its config folder. Only `QtQuick`,
  `Quickshell` and same-directory files (`Api.qml`) are used. The shell's
  private `qs.*` modules are not, so shell updates can't break the widget.
- **The `bar` it gets is `PluginBarApi`, not the real bar:** colors, font,
  position, `run`, tooltips and popout coordination, but no `shellQuote`.
  Notifications use `Quickshell.execDetached` with an argv list, because
  titles and questions come from agents and must never reach a shell.

## Testing without touching the running shell

`test/shell.qml` hosts the widget with a fake bar facade, loading it exactly
as omarchy-shell does, then opens the panel and exits:

```bash
QT_QPA_PLATFORM=offscreen MC_URL=http://127.0.0.1:4200 MC_SHOT=/tmp/panel.png qs -p ui/omarchy/test
```

`MC_ANSWER=<choice>` also answers the first pending ask through the widget's
own API code.
