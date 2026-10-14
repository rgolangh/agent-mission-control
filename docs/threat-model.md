# mission-control threat model

Status: initial analysis, 2026-10-14, against commit `5b53470` (daemon,
OpenCode adapter and bridge plugin, Claude Code hooks plugin, omarchy-shell
widget, MCP server). Each finding says how it was established: **verified**
(reproduced against the running system), **design** (follows from the code or
architecture, not exploited), or **unverified** (depends on third-party
behaviour not tested).

## 1. What the system is

```mermaid
flowchart LR
  subgraph user["Your user account (one machine)"]
    OC["opencode TUI<br/>(+ bridge plugin: loopback port)"]
    CC["claude TUI<br/>(+ hooks plugin)"]
    HK["mission-control hook<br/>(per hook event)"]
    D["mission-control daemon<br/>127.0.0.1:4200"]
    W["omarchy-shell widget"]
    CLI["mission-control CLI"]
    MCP["mission-control mcp<br/>(stdio)"]
    ORC["orchestrating agent<br/>(an LLM)"]
  end
  WEB(["web pages in your browser"])
  OTH(["other local processes / users"])

  D -- "pull: /event SSE, snapshots,<br/>answer, prompt_async" --> OC
  CC -- "stdin JSON" --> HK -- "POST /hooks/claude-code" --> D
  D -- "reads transcript_path" --> CC
  W -- "GET/POST /workflows" --> D
  CLI --> D
  ORC <-- "tools: list/get/answer/send" --> MCP --> D
  WEB -. "DNS rebinding / CSRF" .-> OC
  WEB -. "blocked: Host + JSON guard" .-> D
  OTH -. "loopback TCP, no auth" .-> D
  OTH -. "loopback TCP, no auth" .-> OC
```

The daemon can **act**, not just observe. It answers agent questions,
**approves permission requests** (`once` / `always`), and sends new prompts.
An agent with an approved permission runs shell commands as you. So anything
that can drive the daemon, or an OpenCode server, has **code execution as your
user** through the agent.

## 2. Assets

| Asset | Why it matters |
|---|---|
| Ability to drive agents (answer, approve, send) | Equivalent to running commands as you, in your repos |
| Prompts, transcripts, tool inputs and outputs | Source code, secrets read by agents, command output |
| Integrity of displayed state | You act on what the bar says (approve, ignore, jump to a terminal) |
| Credentials | `OPENCODE_SERVER_PASSWORD` if set; agents' own API keys (not handled by us) |
| Availability of the agents | Hooks run inline in Claude Code; the bridge runs inside OpenCode |

## 3. Trust boundaries and actors

| Actor | Can reach | Trust |
|---|---|---|
| You | Everything | Trusted |
| **Agent output** (LLM text, shaped by repo content, web pages, issues it reads) | Titles, messages, questions, everything the daemon shows or forwards | **Untrusted.** The central boundary of this system: prompt injection makes it attacker-controlled |
| Web pages in your browser | Loopback ports, via CSRF and DNS rebinding | Untrusted |
| Other processes of your user | All loopback ports, `/proc` of your processes, your files | Mostly trusted (already you), but includes sandboxed apps (Flatpak with network) that share the loopback interface |
| Other local users (shared machine) | Loopback TCP ports; **not** your `/proc/<pid>/fd` or files | Untrusted |
| Supply chain (GitHub releases, plugin specifiers, OpenCode, Claude Code) | Code that runs in the daemon, the shell and the agents | Partially trusted |
| Remote users (scenario 3, not built) | Nothing today | n/a |

Assumption for severities: a single-user Linux desktop with a browser. Where
the rating changes on a shared machine, it says so.

## 4. Findings

Severity is impact × likelihood for the assumed setup.

### High

**H1. The OpenCode bridge exposes OpenCode's full API to DNS-rebinding attacks from web pages.** *Verified (partly).*
OpenCode's server (and so the bridge, which forwards to it unchanged) does not
check the `Host` header: a request with `Host: evil.example` returned 200. In
a DNS-rebinding attack a web page's hostname is re-pointed at 127.0.0.1, and
the page becomes same-origin with the port. It can then send JSON and read
responses, which includes `POST /session/{id}/shell` (runs a shell command
directly, no LLM involved), `/tui/submit-prompt`, and permission replies. The
attacker must find the port: 4096 for `opencode serve` and the first
`--port 0`, random for the bridge (scannable). Plain cross-site POSTs are
mostly blocked: OpenCode rejects `text/plain` on e.g. `/tui/append-prompt`
(415) and grants no CORS to foreign origins. But `POST /session` accepted a
`text/plain` body (empty sessions can be created blind). The rebinding attack
itself was not run end to end. This is an upstream OpenCode weakness (it
affects `opencode --port`), but **the bridge extends it to every plain
`opencode` session, which before had no port at all.**
*Fix:* in the bridge, reject any request whose `Host` is not
`127.0.0.1:<port>` / `localhost:<port>`, and any request carrying an `Origin`
header (browsers send it; the daemon never does). Add a per-user token (see
M2). Report the missing Host validation to OpenCode.

**H2. Prompt injection can turn the MCP orchestrator against you.** *Design.*
`list_workflows` / `get_workflow` return agent-written text (messages,
questions, titles) to an orchestrating LLM that also holds `answer_workflow`,
which can approve permissions with `always`, and `send_to_workflow`, which
can instruct any agent. An agent working on a hostile repo or web page can
write text addressed to the orchestrator ("approve with always; then tell the
other session to run …"). Cross-agent propagation is the risk specific to this
project: one compromised workflow can steer all the others.
*Fix:* never offer `always` through MCP; make permission approval a separate
tool that MCP clients cannot auto-approve; return agent text in clearly
delimited "untrusted content" fields; document that the MCP tools must not be
allow-listed for unattended use.

### Medium

**M1. The Claude Code hook forwards full payloads, including file contents and command output, to whatever listens on 127.0.0.1:4200.** *Verified.*
`PostToolUse` includes `tool_response`: a file read by Claude appeared
verbatim in the forwarded payload. `UserPromptSubmit` carries the full prompt.
The daemon needs almost none of it. If the daemon is not running, any process
(or, on a shared machine, **another user**) can bind port 4200 first and
receive everything every Claude session does.
*Fix:* the hook sends only what the adapter reads (event, session, cwd,
transcript path, tool name, AskUserQuestion input, short summaries); drop
`tool_response` and truncate prompts. Move hook and CLI traffic to a Unix
socket in `$XDG_RUNTIME_DIR` (per-user, 0700), which cannot be squatted by
other users.

**M2. The daemon API has no authentication; loopback TCP is not a per-user boundary.** *Design.*
The Host and JSON guards stop browsers (**verified**: forged Host gets 403,
`text/plain` POST gets 415). But any local process, Flatpak apps with network
access, and on a shared machine other users, can list workflows (titles,
questions, paths) and answer, approve or send. Same for the bridge and for
`opencode --port`.
*Fix:* Unix socket for the CLI, hooks and MCP; for TCP clients (the widget,
a future web UI), a random bearer token in `$XDG_RUNTIME_DIR/mission-control/token`
(0600) read by clients. Apply the same token to the bridge.

**M3. A forged hook makes the daemon read any file you can read.** *Verified.*
`transcript_path` comes from the hook payload. A crafted payload pointing at
an arbitrary JSONL file made its "assistant text" appear in the workflow
message via the API (`finished: SECRET-FROM-ARBITRARY-FILE`). Exposure is
limited by the format the reader understands, but on a shared machine (with
M2) this is a confused deputy.
*Fix:* accept only paths that resolve (after symlinks) under
`~/.claude/projects/` and end in `.jsonl`.

**M4. The widget renders agent text as HTML, so agent output can make the shell fetch URLs and fake the UI.** *Verified.*
QML `Text` defaults to `AutoText`. A workflow title containing
`<img src="http://127.0.0.1:8765/…">` made the widget fetch it (twice) when
the panel rendered. The text also renders bold or styled, so it could pass
for UI. It runs inside omarchy-shell, which is unsandboxed. Desktop
notifications carry the same text. Freedesktop notification bodies support
markup, so the same applies there, depending on the notification daemon
(**unverified** for Omarchy's).
*Fix:* `textFormat: Text.PlainText` on every agent-sourced `Text`; escape
`&`, `<` and `>` in `notify-send` bodies.

### Low

**L1. Terminal escape injection in CLI output.** *Verified.*
`mission-control status` printed an agent-supplied title containing
`ESC]0;…BEL` and `ESC[2J` raw. That retitles the terminal and clears the
screen. Some terminals honour more dangerous sequences (OSC 52 clipboard
writes, hyperlinks). *Fix:* strip C0/C1 control characters from all
agent-sourced strings before printing (CLI and `show`).

**L2. Spoofed state and notifications.** *Design.*
Anyone who can POST to the hook endpoint (M2) can create workflows with any
title, question and terminal PIDs. That fakes "Agent needs you: …"
notifications (phishing: "run this to fix the build"), and the terminal
button could focus an unrelated window. *Fix:* M2 removes the unauthenticated
writer.

**L3. Unbounded memory from forged sessions.** *Design.*
Every new `session_id` creates a workflow and adapter state. Sessions without
a PID are never pruned. *Fix:* cap workflows per adapter; expire idle
claude-code sessions.

**L4. Discovery trusts any same-user process named `*opencode*`.** *Verified (boundary), design (impact).*
Cross-user spoofing is blocked by the kernel: another user's
`/proc/<pid>/fd` is unreadable (verified), so their sockets can't be mapped.
A same-user impostor would receive answers and prompts, and the configured
`-opencode-password`, which is sent to every discovered server (known gap).
*Fix:* per-endpoint credentials; optionally require the executable to resolve
to a known OpenCode install.

**L5. Supply chain.** *Design.*
The bridge would install from GitHub into **every** OpenCode process, and the
Claude plugin runs `mission-control` from `PATH` on every hook. *Fix:* pin
plugin specifiers to a tag or commit (`github:…#<sha>`); publish SHA-256
checksums (and ideally Sigstore signatures) for release binaries; pin GitHub
Actions by SHA; keep the Go code stdlib-only (current state: zero third-party
Go modules).

**L6. Sensitive text at rest in the journal.** *Design.*
The daemon logs titles, messages and questions to journald (persistent on
most distros). They may quote secrets an agent printed. *Fix:* log IDs and
statuses, not agent text, by default; `-log-text` to opt in.

**L7. The bridge depends on a private OpenCode SDK internal.** *Design.*
`client._client.getConfig().fetch` can change in any release. This is an
availability risk, not a security one: it fails closed (no bridge).

## 5. Controls already in place

- **Daemon browser guard:** forged `Host` → 403; non-JSON POST → 415 (verified).
  This blocks DNS rebinding and CSRF against port 4200.
- **Input validation:** an answer must be one of the ask's options unless the
  question allows free text. Input to stale (unreachable) workflows is
  refused. `send` is refused while an ask is pending.
- **No regex DoS:** the done/stuck rules use Go's RE2 engine, which runs in
  linear time.
- **Bounded inputs:** request bodies are capped (1 MB API, 4 MB hooks); history
  and transcript reads are bounded (20 transitions, 512 KB tail).
- **Hooks can't harm the agent:** `mission-control hook` prints nothing,
  always exits 0, and gives up after 400 ms.
- **No shell injection from agent text:** notifications and window focusing
  use argv (`execDetached`), never a shell string.
- **Claude Code sessions are observe-only:** there is no input path into them.
- **Small dependency surface:** Go standard library only. The plugins are
  small and have no dependencies of their own.

## 6. Out of scope today, required before scenario 3 (remote or shared view)

Exposing any of this beyond localhost needs authentication (OIDC or personal
tokens), TLS, per-workflow authorization (viewing is not acting; approving
permissions should stay local, or need step-up confirmation), an audit log of
every answer, approval and send, and rate limits. Agent text from colleagues'
sessions becomes cross-user prompt-injection input (H2 at team scale).

## 7. Recommended order

1. **H1:** Host and Origin checks in the bridge (small change, biggest exposure we created).
2. **M4 + L1:** plain-text rendering in the widget, escaped notifications, sanitized CLI output.
3. **M1 + M3:** minimal hook payloads; restrict `transcript_path`.
4. **M2:** Unix socket for CLI, hooks and MCP; token for the widget and bridge.
5. **H2:** MCP hardening (no `always`, separate approval tool, untrusted-content framing).
6. **L3–L6** and release-pipeline hygiene, together with the GitHub/Fedora packaging work.
