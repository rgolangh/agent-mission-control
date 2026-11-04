# agent-mission-control

One place to see the state of all your coding-agent sessions (who is
working, who is **waiting on you**, who finished, who is stuck) and to answer
them without hunting for the right terminal tab.

![agent-mission-control CLI: see which agents need you, read a question, answer it, approve a permission and nudge a stuck agent](docs/demo/cli.gif)

The unit is the **workflow**, not the tool. Every agent session reports into
one daemon through a small per-tool adapter. The daemon tracks state, works
out whether an idle agent is *done*, *stuck* or *waiting for you*, and lets
you (or an orchestrating agent, over MCP) answer it.

## What it supports

| Source | How it connects | You can |
|---|---|---|
| **OpenCode** | Daemon reads OpenCode's own HTTP/SSE API. A tiny plugin gives a plain `opencode` an authenticated local port so every session is discovered automatically | See state, answer questions, approve or reject permissions, send follow-ups |
| **Claude Code** | A hooks-only Claude Code plugin pushes session events to the daemon | See state, questions and permission prompts (answer in the terminal; Claude Code has no input API) |
| **Codex** | Daemon connects to a shared Codex app-server Unix socket | See state, answer questions, approve or reject command and supported file requests, send follow-ups |

With `amc install --enable`, the AMC service discovers Codex's default shared
app-server socket automatically when it appears. Start Codex normally on a
version configured to use its shared daemon. `amc list` and the desktop UI
then work without a separate AMC terminal. Codex versions or sessions using a
private app-server are not discovered through this socket.

To connect a Codex terminal through an explicitly selected shared app-server:

```bash
codex app-server --listen unix:///tmp/codex-amc.sock
codex --remote unix:///tmp/codex-amc.sock
amc serve -codex-socket /tmp/codex-amc.sock
```

The adapter watches sessions loaded on that server. Existing sessions in a
different Codex process are not automatically attached. Codex subagents are
excluded because Codex does not accept direct messages to them. Structured Codex
questions use an experimental app-server interface. Only the listed approval
choices are offered; other approval request types must be answered in Codex.
File approvals are available only when the complete proposed diff was observed
and is at most 32 KiB; otherwise, review and answer in Codex.
For a Codex app-server using its default control socket, pass
`-codex-socket default` instead of a path. This also honors `CODEX_HOME`.

For adapters that report token usage (currently Codex), `amc get` displays the
most recently observed counts, including cached input tokens. The daemon
estimates an expiry 30 minutes after the latest
usage update and warns five minutes before it, if that request used cached
tokens. Desktop clients notify once per observation. Configure the assumptions with `-cache-lifetime` and
`-cache-warning-lead`, or disable warnings with
`-cache-warning-lead=0`. The backend does not expose cache expiry, so
the warning cannot guarantee the cache is still warm or that the next request
will hit it. A follow-up itself uses tokens; send one only if it is useful.

Ways to look at it:
- **CLI:** `status`, `show`, `answer`, `send`.
- **MCP server:** for an orchestrating agent.
- **Desktop UI:** a widget for [Omarchy](https://omarchy.org)'s shell, or a
  [GNOME Shell extension](ui/gnome/README.md). Both have an indicator, the
  workflow list, notifications, and a jump to the agent's terminal.

<img src="docs/demo/promo.gif" width="720" alt="Three agents working; one asks which session store to use, the Omarchy bar icon turns amber and notifies, the panel opens and the answer Redis is picked, then all three finish and the bar goes calm">

([mp4](docs/demo/promo.mp4))

**Done vs stuck vs waiting** is decided by ordered, evidence-quoting rules
(no LLM). They read how the turn ended: errors, finish reason, a closing
question or an optional offer, "I couldn't…" blockers, open todos. A session
with no activity at all for 15 minutes is flagged as hung.

## Requirements

- Linux (discovery and liveness read `/proc`)
- Go 1.27+ to build
- OpenCode, Claude Code, and/or Codex

## Install

### From a package (Arch / Omarchy, Fedora)

The package puts `amc` in `/usr/bin`, a systemd user service in
`/usr/lib/systemd/user`, the GNOME Shell extension in
`/usr/share/gnome-shell/extensions` and the agent integrations and Omarchy
widget in `/usr/share/agent-mission-control`. Then, once per user (not as root)
(maintainers: [packaging/README.md](packaging/README.md)):

```bash
# Arch / Omarchy: the release's package (aarch64: swap x86_64 for aarch64). Download it
# first: pacman wants a signature for a URL, but not for a local file.
# The AUR package agent-mission-control-bin is coming (AUR sign-ups are closed for now).
curl -LO https://github.com/rgolangh/agent-mission-control/releases/download/v0.1.4/agent-mission-control-bin-0.1.4-1-x86_64.pkg.tar.zst
sudo pacman -U ./agent-mission-control-bin-0.1.4-1-x86_64.pkg.tar.zst
# Fedora (COPR)
sudo dnf copr enable rgolan/agent-mission-control && sudo dnf install agent-mission-control

amc install --enable   # enable the service, add the agent integrations and desktop UI
amc doctor
```

`.pkg.tar.zst` and `.rpm` files are also attached to every
[release](https://github.com/rgolangh/agent-mission-control/releases/latest)
(`sudo pacman -U ./…` / `sudo dnf install ./…`). With the packaged binary,
`amc install` uses the packaged service and GNOME extension instead of
per-user copies (and removes per-user copies an earlier install wrote, which
would shadow them), and links the integrations to the package's files, so
upgrades update them. After a package upgrade, `systemctl --user restart
agent-mission-control` (the RPM restarts it for you) and, on Omarchy,
`omarchy-restart-shell`.

On Omarchy, `amc install` also puts the bar widget in place, at the
daemon's version ([details](ui/omarchy/README.md#install)).

### From the release binary

Download the binary for your machine from the
[latest release](https://github.com/rgolangh/agent-mission-control/releases/latest),
check it, and let it install itself:

```bash
curl -LO https://github.com/rgolangh/agent-mission-control/releases/latest/download/amc-linux-amd64
curl -LO https://github.com/rgolangh/agent-mission-control/releases/latest/download/SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
chmod +x amc-linux-amd64
./amc-linux-amd64 install --dry-run   # see what it will do
./amc-linux-amd64 install
amc doctor
```

Or from source: `CGO_ENABLED=0 GOBIN=~/.local/bin go install -trimpath github.com/rgolangh/agent-mission-control/cmd/amc@latest`,
then `amc install`.

`install` puts in place, for your user only:
- the binary, in `~/.local/bin` (not with a package: that one is in `/usr/bin`);
- a systemd user service, which it starts (with a package: enables the packaged one);
- the OpenCode bridge plugin, if OpenCode is installed;
- the Claude Code hooks plugin, if Claude Code is installed
  (with a package, these two and the Omarchy widget are links to the package's files);
- the desktop UI it detects: the omarchy-shell widget (unless `omarchy plugin
  add` manages it) or the GNOME Shell extension (unless packaged).

It only adds files and never edits your existing configuration. It refuses to
overwrite anything it didn't write (unless `--force`) and leaves symlinks
alone. Commands that change another tool's settings, like enabling a shell
extension, are printed for you to run, or run with `--enable`. Use
`--only opencode,claude-code` to pick components. `amc uninstall`
removes exactly what was installed and unchanged.

## Use

```bash
amc list                   # every workflow (also: amc status)
amc get <id>               # pending questions + history (also: amc show)
amc answer <id> <choice>   # option label or number; for permissions: once | always | reject
amc send <id> <text...>    # follow-up message to a finished or stuck agent
```

`amc send` refuses to send while a structured question or approval is pending.
Use `amc get <id>` to inspect it, then `amc answer <id> <choice>` to respond.
This keeps a follow-up from accidentally approving a request or bypassing a question.
It is also refused for stale workflows (their server is unreachable) and for
Claude Code workflows, which are observe-only: answer those in their terminal.

MCP (e.g. Claude Code): `claude mcp add agent-mission-control -- amc mcp`.
Tools: `list_workflows`, `get_workflow`, `answer_workflow`, `resolve_permission`
(`once` / `reject` only), `send_to_workflow`.

## Security

agent-mission-control can approve agent permissions, which run commands as you, so
access is locked to your user:
- **Unix socket:** the CLI, hooks and MCP use a socket only your user can reach.
- **Token on TCP:** the widget's TCP port needs a per-start bearer token.
- **Bridge:** the OpenCode bridge needs a token and is protected against DNS rebinding.
- **Agent text is untrusted:** it's rendered as plain text, never passed to a shell, and labelled untrusted for MCP clients.

Read [SECURITY.md](SECURITY.md) before allow-listing MCP tools. The full
analysis is in [docs/threat-model.md](docs/threat-model.md).

## Status

Early and moving fast; interfaces may change. Tested against OpenCode 1.18
and Claude Code 2.1 on Linux. Planned: more adapters
(GitHub PRs, Jira) linked to the sessions that produced them, and a shared
web view.

Using it day to day? Share install friction, workflow misclassifications, and
which agent/desktop combinations you use in a
[field-feedback issue](https://github.com/rgolangh/agent-mission-control/issues/new).
Please don't attach raw agent transcripts or logs; they may contain secrets.

## Development

```bash
go vet ./... && go test -race ./...
QT_QPA_PLATFORM=offscreen qs -p ui/omarchy/test   # headless widget harness
```

## License

[Apache-2.0](LICENSE)
