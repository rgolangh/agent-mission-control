# Agent Mission Control: omarchy-shell bar widget

> **Needs the `amc` daemon**, which this widget only displays: install it from
> [agent-mission-control](https://github.com/rgolangh/agent-mission-control#install) (`amc install --enable` then
> sets it up for OpenCode and Claude Code). Without it the widget shows a dim
> `✕` and how to install it.
>
> [rgolangh/omarchy-agent-mission-control](https://github.com/rgolangh/omarchy-agent-mission-control)
> is a read-only mirror of [`ui/omarchy`](https://github.com/rgolangh/agent-mission-control/tree/main/ui/omarchy),
> updated on each release. Open issues and pull requests in
> [agent-mission-control](https://github.com/rgolangh/agent-mission-control).

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

When the daemon doesn't answer, the count becomes a dim `✕` and the panel
says why, with one button: `amc` not installed (link to the releases), installed
but `amc install --enable` never run (copy the command), service stopped
(`systemctl --user start agent-mission-control.service`), token missing or
refused, or not answering on its port (restart). It tells these apart from the
HTTP status, whether the token file exists, and a small `sh` probe (every 10s
at most, only while down) for the `amc` binary, the systemd unit and the
daemon's socket.

## Install

Install the daemon first ([agent-mission-control](https://github.com/rgolangh/agent-mission-control):
the `.pkg.tar.zst` from the [latest release](https://github.com/rgolangh/agent-mission-control/releases/latest)
(download it, then `sudo pacman -U ./<file>`), or the release binary). Then run `amc install
--enable`: it writes this widget from the files embedded in the `amc`
binary into `~/.config/omarchy/plugins/agent-mission-control`, so it always
matches the daemon it talks to, and places it in the bar. After a package
upgrade, run `amc install` again to update it.

Or add the widget with Omarchy, and install the daemon as above:

```bash
omarchy plugin add https://github.com/rgolangh/omarchy-agent-mission-control --enable
```

Omarchy then updates it (`omarchy plugin update`), and `amc install` leaves
it alone. Its versions follow the daemon's releases: update both together.

### Remove

```bash
omarchy plugin remove agent-mission-control   # the widget, if added with omarchy plugin add
amc uninstall                                 # what amc install put in place (widget, integrations, service)
```

then remove the package or `~/.local/bin/amc` ([details](https://github.com/rgolangh/agent-mission-control#install)).

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

The daemon-down classification (same table as the GNOME `model.js` tests);
`qs` exits 0 either way, so check the output:

```bash
QT_QPA_PLATFORM=offscreen MC_TEST=states qs -p ui/omarchy/test 2>&1 | grep 'states: all passed'
# live, with an empty runtime dir: no token, no socket
XDG_RUNTIME_DIR=$(mktemp -d) QT_QPA_PLATFORM=offscreen MC_EXPECT_STATE=not-running qs -p ui/omarchy/test
```
