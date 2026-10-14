# Mission control: omarchy-shell bar widget

A bar icon that counts the agent workflows that need you (waiting on an
answer, stuck or failed; in the theme's urgent color, pulsing while one is
waiting on an answer), and a panel listing every workflow, most urgent first,
where you can answer questions and approve permissions with one click, or send
a finished or stuck agent a follow-up. A desktop notification fires when an
agent starts needing you.

It is a client of the mission-control daemon's HTTP API, so the daemon must be
running (`mission-control serve -discover`). The daemon's TCP port requires a
bearer token, which the widget reads from
`$XDG_RUNTIME_DIR/mission-control/token` (regenerated on each daemon start;
the widget notices). Agent text is always rendered as plain text, never HTML. It polls `GET /workflows` (every
2s; every 1s while the panel is open). Answering never changes what the widget
shows by itself: the next poll shows what the agent actually did.

## Install (development: symlink this directory)

```bash
ln -s "$PWD/ui/omarchy" ~/.config/omarchy/plugins/mission-control
omarchy-shell shell rescanPlugins
omarchy plugin enable mission-control
omarchy bar move   # place it in a bar section
```

Settings (in `~/.config/omarchy/shell.json`, or through the bar's widget
settings): `apiUrl` (default `http://127.0.0.1:4200`), `pollIntervalMs`,
`notify`, `hideWhenDown`.

Mouse: left click toggles the panel, right click refreshes. Esc closes it.

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
