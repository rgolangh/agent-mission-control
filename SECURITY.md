# Security

agent-mission-control can **act** on your coding agents, not just watch them. It
answers their questions, approves their permission requests, and sends them
prompts. An agent with an approved permission runs commands as you. So treat
anything that can talk to the daemon as able to run code as your user, and
the design below is built around keeping that to *you*.

The full analysis, with findings, evidence, fixes and what is still open, is
in [docs/threat-model.md](docs/threat-model.md).

## Reporting a vulnerability

Please **do not open a public issue** for security problems. Use GitHub's
private vulnerability reporting on this repository (Security → "Report a
vulnerability"). Include what you did, what happened, and the version
(`git describe` or the release tag). You should get an acknowledgement within
a week.

Supported versions: this project is pre-1.0. Fixes land on `main` and in the
next release; older releases are not patched.

## Security model in brief

**Who is trusted.** You, and processes running as your user. Not trusted:
**anything an agent writes** (agents read repositories, web pages and issues,
so their output can be steered by prompt injection), web pages in your
browser, other users on the machine, and sandboxed apps that share the
loopback network.

**How the daemon is reached.**

| Channel | Used by | Protection |
|---|---|---|
| Unix socket `$XDG_RUNTIME_DIR/agent-mission-control/api.sock` | CLI, hooks, MCP | Only your user can enter the directory (0700) or connect to the socket (0600); it can't be squatted by another user |
| TCP `127.0.0.1:4200` | Desktop widget | Bearer token, regenerated on every daemon start, in `$XDG_RUNTIME_DIR/agent-mission-control/token` (0600); `Host` must be loopback (DNS rebinding); POST must be JSON (cross-site requests) |
| OpenCode bridge port (plugin, random port) | Daemon → plain `opencode` | Per-process token file (0600); `Host` must be loopback; any `Origin` header refused |

**What leaves your agents.** The Claude Code hook strips each event down to
what agent-mission-control needs before sending it: no file contents, no command
output, prompts cut to a title. Transcripts are read only from Claude Code's
own `projects` directory.

**Agent text is data.** The widget renders it as plain text (never HTML),
notifications escape it, and nothing passes it to a shell. The MCP server
labels it as untrusted. Permission approvals go through a separate
`resolve_permission` tool that offers only `once` or `reject`, never
`always`.

**Observe-only where there is no safe input path.** Claude Code sessions can
be watched but not driven from agent-mission-control.

## Hardening advice

- **Never allow-list `resolve_permission` or `send_to_workflow`** in an MCP
  client for unattended use. Let the client ask you each time. Agent text can
  be written to persuade the orchestrating model.
- **Prefer plain `opencode` over `opencode --port N` / `opencode serve`.**
  With the bridge plugin, plain `opencode` gets an authenticated,
  rebinding-proof port. OpenCode's own server does not validate the `Host`
  header, so a web page can attack it through DNS rebinding. If you use a
  password-protected native server, configure its URL with `-opencode`; the
  password is not sent to auto-discovered endpoints.
- **Do not bind the daemon beyond loopback** (`-listen 0.0.0.0:…`). The token
  protects the API, but there is no TLS, and remote access is not part of the
  design yet.
- **Pin what you install**: the OpenCode plugin specifier to a tag or commit,
  and release binaries checked against their published checksums.
- The daemon logs only workflow IDs, statuses and rules. `serve -log-text`
  also logs titles and messages (handy when tuning the rules); the journal
  keeps them, and agents may print secrets.

## Known limitations

See "Remaining work" in the threat model: same-user impostors may still be
mistaken for a healthy native OpenCode server (L4), and release signatures
(L5). Workflow/session state and OpenCode snapshot responses/event frames are
bounded.
