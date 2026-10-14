# mission-control

One place to see the state of all your coding-agent sessions (who is
working, who is **waiting on you**, who finished, who is stuck) and to answer
them without hunting for the right terminal tab.

```
$ mission-control status
STATUS       TITLE                     WHERE              ID        UPDATED  MESSAGE
needs_input  Refactor auth middleware  ~/src/api          dmIe4nhh  12s ago  asks: Which session store should it use?
stuck        Fix flaky sync test       ~/src/sync         QgSjmCSR  3m ago   blocked: I couldn't run the integration tests
running      Write release notes       ~/src/docs         lqUuRN4g  now      working
done         Bump dependencies         ~/src/api          TZsqhlgs  20m ago  finished: All tests pass.
```

The unit is the **workflow**, not the tool. Every agent session reports into
one daemon through a small per-tool adapter. The daemon tracks state, works
out whether an idle agent is *done*, *stuck* or *waiting for you*, and lets
you (or an orchestrating agent, over MCP) answer it.

## What it supports

| Source | How it connects | You can |
|---|---|---|
| **OpenCode** | Daemon reads OpenCode's own HTTP/SSE API. A tiny plugin gives a plain `opencode` an authenticated local port so every session is discovered automatically | See state, answer questions, approve or reject permissions, send follow-ups |
| **Claude Code** | A hooks-only Claude Code plugin pushes session events to the daemon | See state, questions and permission prompts (answer in the terminal; Claude Code has no input API) |

Ways to look at it:
- **CLI:** `status`, `show`, `answer`, `send`.
- **MCP server:** for an orchestrating agent.
- **Desktop widget:** for [Omarchy](https://omarchy.org)'s shell, with a bar icon, a panel, notifications, and a jump to the agent's terminal.

**Done vs stuck vs waiting** is decided by ordered, evidence-quoting rules
(no LLM). They read how the turn ended: errors, finish reason, a closing
question or an optional offer, "I couldn't…" blockers, open todos. A session
with no activity at all for 15 minutes is flagged as hung.

## Requirements

- Linux (discovery and liveness read `/proc`)
- Go 1.27+ to build (standard library only, no third-party modules)
- OpenCode and/or Claude Code

## Install (from source)

```bash
git clone <this repo> && cd mission-control
CGO_ENABLED=0 go build -trimpath -o ~/.local/bin/mission-control ./cmd/mission-control
```

Run the daemon as a systemd user service:

```ini
# ~/.config/systemd/user/mission-control.service
[Unit]
Description=mission-control: agent workflow state daemon

[Service]
ExecStart=%h/.local/bin/mission-control serve -discover
Restart=on-failure

[Install]
WantedBy=default.target
```

```bash
systemctl --user enable --now mission-control
```

Connect your agents:

```bash
# OpenCode: every new `opencode` becomes discoverable
mkdir -p ~/.config/opencode/plugins
ln -s "$PWD/integrations/opencode/mission-control-bridge.js" ~/.config/opencode/plugins/

# Claude Code: hooks plugin (loads as mission-control@skills-dir in new sessions)
ln -s "$PWD/integrations/claude-code" ~/.claude/skills/mission-control
```

Omarchy desktop widget: see [ui/omarchy/README.md](ui/omarchy/README.md).

## Use

```bash
mission-control status                 # every workflow
mission-control show <id>              # pending questions + history (<id>: ID column, any unique suffix)
mission-control answer <id> <choice>   # option label or number; for permissions: once | always | reject
mission-control send <id> <text...>    # follow-up message to a finished or stuck agent
```

MCP (e.g. Claude Code): `claude mcp add mission-control -- mission-control mcp`.
Tools: `list_workflows`, `get_workflow`, `answer_workflow`, `resolve_permission`
(`once` / `reject` only), `send_to_workflow`.

## Security

mission-control can approve agent permissions, which run commands as you, so
access is locked to your user:
- **Unix socket:** the CLI, hooks and MCP use a socket only your user can reach.
- **Token on TCP:** the widget's TCP port needs a per-start bearer token.
- **Bridge:** the OpenCode bridge needs a token and is protected against DNS rebinding.
- **Agent text is untrusted:** it's rendered as plain text, never passed to a shell, and labelled untrusted for MCP clients.

Read [SECURITY.md](SECURITY.md) before allow-listing MCP tools. The full
analysis is in [docs/threat-model.md](docs/threat-model.md).

## Status

Early and moving fast; interfaces may change. Tested against OpenCode 1.18
and Claude Code 2.1 on Linux. Planned: packaging (release
binaries, `mission-control install`), a GNOME Shell extension, more adapters
(GitHub PRs, Jira) linked to the sessions that produced them, and a shared
web view.

## Development

```bash
go vet ./... && go test -race ./...
QT_QPA_PLATFORM=offscreen qs -p ui/omarchy/test   # headless widget harness
```

[CLAUDE.md](CLAUDE.md) is the design log: why things are shaped the way they
are, and what was verified against which tool version.
