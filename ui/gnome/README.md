# Mission control: GNOME Shell extension

A top-bar indicator for GNOME Shell 45 and later:
- **Indicator:** counts the coding-agent workflows that need you (waiting on
  an answer, stuck or failed), in red when any do.
- **Menu:** live workflow cards, most urgent first, with **All**, **Needs you**,
  **Waiting**, and **Running** views. All is the default, so idle and completed
  workflows remain visible. Answer a question with one click, type a reply or follow-up,
  or jump to the agent's terminal window. Running workflows show a bidirectional
  Larson scanner in their cards. Updates preserve in-progress reply text and
  caret position, as well as multi-select choices.
- **Keyboard:** `Ctrl+1`–`Ctrl+4` switch views; `Ctrl+R` refreshes. Shortcuts
  are suspended while a reply field has focus.
- **Global shortcut:** `Super+Alt+M` toggles the menu. Change or disable it
  by writing another accelerator list, e.g.
  `dconf write /org/gnome/shell/extensions/mission-control/toggle-menu "['<Super><Shift>m']"`;
  run `dconf write /org/gnome/shell/extensions/mission-control/toggle-menu "[]"`
  to disable it.
- **Notifications:** a desktop notification when an agent starts needing you.

It is a thin client of the mission-control daemon. It reads the daemon's
token from `$XDG_RUNTIME_DIR/mission-control/token` and polls
`http://127.0.0.1:4200` every 2 seconds. All agent text is shown as plain text,
never markup.

## Install

`mission-control install` puts it in
`~/.local/share/gnome-shell/extensions/mission-control@rgolangh.github.io` when it
detects GNOME Shell. Then:

```bash
gnome-extensions enable mission-control@rgolangh.github.io
```

(`mission-control install --enable` runs that for you.) On Wayland, GNOME Shell
only loads a newly installed extension after you log out and back in.

Each release also has `mission-control@rgolangh.github.io.shell-extension.zip`
for `gnome-extensions install`.

To package a local build, include `schemas/gschemas.compiled` in the extension
bundle along with the schema XML; `make gnome-schemas` regenerates it after
schema changes.

## Layout

| File | What | Tested by |
|---|---|---|
| `model.js` | Pure logic: urgency order, views, counts, summaries, which workflows newly need you | `gjs -m test/model.test.js` (also in CI) |
| `client.js` | libsoup 3 client, token handling and re-reading after a daemon restart | `gjs -m test/client.test.js` against a running daemon |
| `extension.js` | Panel button, menu, notifications, window focusing | GNOME Shell only |

Debug a running copy with `journalctl -f -o cat /usr/bin/gnome-shell`.
