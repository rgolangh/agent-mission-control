# agent-mission-control threat model

Status: analysis of 2026-10-14 (against `5b53470`), updated the same day
after fixing every finding rated Medium or above. Re-reviewed 2026-10-15
(against `84b4cd9` plus the GNOME extension, installer and release work):
earlier fixes re-verified live, findings N1–N8 fixed or mitigated, L1 and L6
fixed (section 8). Each finding says how it
was established: **verified** (reproduced against the running system),
**design** (follows from the code or architecture, not exploited), or
**unverified** (depends on third-party behaviour not tested). Each fixed
finding also says how the fix was verified.

| ID | Finding | Severity | Status |
|---|---|---|---|
| H1 | Bridge exposes OpenCode to DNS rebinding | High | **Fixed** |
| H2 | Prompt injection through the MCP orchestrator | High | **Mitigated** (inherent residual risk) |
| M1 | Hook forwards file contents / command output to a squattable port | Medium | **Fixed** |
| M2 | Unauthenticated daemon API on loopback TCP | Medium | **Fixed** |
| M3 | Forged hook makes the daemon read arbitrary files | Medium | **Fixed** |
| M4 | Widget renders agent text as HTML | Medium | **Fixed** |
| L1, L6 | Terminal escapes in CLI output; agent text in the journal | Low | **Fixed** 2026-10-15 |
| L2, L4–L5, L7 | See below | Low | Open (L2 now limited to your own processes by M2; L4 mitigated; L5 partly done) |
| L3 | Unbounded workflow and adapter state | Low | **Fixed** 2026-10-15 |
| N1–N8 | Review of 2026-10-15, see section 8 | Bug / Low | **Fixed or mitigated** |

## 1. What the system is

```mermaid
flowchart LR
  subgraph user["Your user account (one machine)"]
    OC["opencode TUI<br/>(+ bridge plugin: loopback port)"]
    CC["claude TUI<br/>(+ hooks plugin)"]
    HK["amc hook<br/>(per hook event)"]
    D["agent-mission-control daemon<br/>unix socket (0600) +<br/>127.0.0.1:4200 (token)"]
    W["omarchy-shell widget"]
    CLI["amc CLI"]
    MCP["agent-mission-control mcp<br/>(stdio)"]
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
  WEB -. "blocked: Host + Origin checks, token" .-> OC
  WEB -. "blocked: Host + JSON guard, token" .-> D
  OTH -. "blocked: socket perms / token" .-> D
  OTH -. "bridge: token; opencode --port: still open (upstream)" .-> OC
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

> **Fixed.** The bridge refuses a wrong `Host` (403) and any `Origin` (403),
> and requires `Authorization: Bearer <token>` on everything except
> `GET /global/health` (401 otherwise). The token is random per process and is
> written with its port to `$XDG_RUNTIME_DIR/agent-mission-control/opencode/<pid>.token`
> (dir 0700, file 0600; removed on exit). The daemon reads it using the PID
> discovery already knows, and sends it only to that port. This also gives
> bridged sessions per-endpoint credentials (part of L4). *Verified live:*
> unauthenticated `POST /session/x/shell` → 401, `Host: evil.example` → 403,
> `Origin` with a valid token → 403. The daemon discovered, authenticated and
> drove a question/answer loop through it. *Remaining:* `opencode --port N`
> and `opencode serve` themselves still lack Host validation (upstream; to be
> reported). Use the bridge (plain `opencode`), or set
> `OPENCODE_SERVER_PASSWORD`.

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

> **Mitigated.** `answer_workflow` refuses permission requests. A separate
> `resolve_permission` tool accepts only `once` or `reject`; `always` is
> unreachable over MCP. Being separate, an MCP client can auto-approve
> answering questions while still asking you before each permission decision.
> Every result carrying agent text includes a `notice` saying it is untrusted
> data, and the server instructions tell the model never to act on it. *Verified* by tests
> (`TestMCPPermissionsNeedTheirOwnToolAndNeverAlways`). *Residual risk:* a
> model can still be persuaded by text it reads. Do not allow-list
> `resolve_permission` or `send_to_workflow` for unattended use.

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

> **Fixed.** `claudecode.Minimize` runs inside the hook process, before
> anything is sent. It allowlists the fields the adapter reads, drops
> `tool_response` and all other tool input (keeping only short `command` /
> `file_path`-style summaries and the AskUserQuestion input), caps prompts at
> 300 bytes, and caps the final message to its first 2 KB and last 8 KB. The
> hook posts to the Unix socket (see M2). *Verified* by test
> (`TestMinimizeDropsContent`: file body, command output and unknown fields
> are gone) and live (this session's hooks reach the daemon over the socket).

**M2. The daemon API has no authentication; loopback TCP is not a per-user boundary.** *Design.*
The Host and JSON guards stop browsers (**verified**: forged Host gets 403,
`text/plain` POST gets 415). But any local process, Flatpak apps with network
access, and on a shared machine other users, can list workflows (titles,
questions, paths) and answer, approve or send. Same for the bridge and for
`opencode --port`.
*Fix:* Unix socket for the CLI, hooks and MCP; for TCP clients (the widget,
a future web UI), a random bearer token in `$XDG_RUNTIME_DIR/agent-mission-control/token`
(0600) read by clients. Apply the same token to the bridge.

> **Fixed.** The daemon serves on `$XDG_RUNTIME_DIR/agent-mission-control/api.sock`
> (dir 0700, socket 0600; it refuses a runtime dir others can access, and
> refuses to start if another instance holds the socket). The CLI, hooks and
> MCP default to it. TCP (`-listen`, for the widget; empty disables it)
> requires a bearer token that is regenerated on every start and written
> 0600 only after the port is bound. Token comparison is constant-time.
> Token files with looser permissions are refused. *Verified live:* modes as
> above; TCP without token → 401, with token → 200; the widget authenticates
> by reading the token file. Tests: `TestRequireToken`, `TestCallOverUnixSocket`.

**M3. A forged hook makes the daemon read any file you can read.** *Verified.*
`transcript_path` comes from the hook payload. A crafted payload pointing at
an arbitrary JSONL file made its "assistant text" appear in the workflow
message via the API (`finished: SECRET-FROM-ARBITRARY-FILE`). Exposure is
limited by the format the reader understands, but on a shared machine (with
M2) this is a confused deputy.
*Fix:* accept only paths that resolve (after symlinks) under
`~/.claude/projects/` and end in `.jsonl`.

> **Fixed.** `transcript_path` is used only if it is absolute, ends in
> `.jsonl`, and resolves (symlinks on both sides) inside
> `~/.claude/projects` or `$CLAUDE_CONFIG_DIR/projects`. Otherwise it is
> ignored. *Verified live:* the same forged hook now yields "could not read the
> transcript", with no file content. Test
> `TestTranscriptPathMustBeInsideProjectsDir` covers outside paths, `..`
> escapes, a symlink inside pointing outside, and non-JSONL files.

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

> **Fixed.** Every `Text` in the widget (all 17) is `Text.PlainText`, and
> notification summary and body are markup-escaped (argv, never a shell).
> *Verified live:* the same `<img>` title now renders literally and the
> listener received 0 requests (2 before).

### Low

**L1. Terminal escape injection in CLI output.** *Verified.*
`amc status` printed an agent-supplied title containing
`ESC]0;…BEL` and `ESC[2J` raw. That retitles the terminal and clears the
screen. Some terminals honour more dangerous sequences (OSC 52 clipboard
writes, hyperlinks). *Fix:* strip C0/C1 control characters from all
agent-sourced strings before printing (CLI and `show`).

> **Fixed** (2026-10-15, with N6). `adapter.CleanText` removes C0/C1
> controls, bidi overrides and isolates, zero-width characters and invalid
> UTF-8. `Store.Apply` cleans every event's message, title, location and
> asks once, on the way in, so the CLI, both UIs, MCP and the log all get
> clean text; `judge` (which reads transcripts directly) cleans what it
> prints. *Verified* by tests (`TestCleanText`, `TestApplyStoresCleanText`)
> and live: `judge` on a file with `ESC]0;…BEL` prints no ESC byte.

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

> **Fixed** (2026-10-15). The store caps workflows at 1,000 per adapter and
> 2,000 total, evicting the oldest completed workflow before rejecting new
> active work; the subagent parent map is capped at 10,000 links. Claude Code
> adapter state is capped at 1,000 sessions and expires after 24 hours without
> hooks when the process is not live. Completed/failed Claude Code workflows
> are pruned after 24 hours; active and waiting workflows are retained.
> *Verified* by `TestWorkflowCountIsBounded`, `TestParentLinksAreBounded`,
> `TestPruneIdleOnlyRemovesCompletedWorkflows`, `TestExpireIdleSessions`, and
> `TestNormalizeBoundsSessionState`.

**L4. Discovery trusts any same-user process named `*opencode*`.** *Verified (boundary), design (impact).*
Cross-user spoofing is blocked by the kernel: another user's
`/proc/<pid>/fd` is unreadable (verified), so their sockets can't be mapped.
A same-user impostor could receive answers and prompts, and before the
mitigation the configured `-opencode-password` was sent to every discovered
server (known gap).
*Fix:* per-endpoint credentials; optionally require the executable to resolve
to a known OpenCode install.

> **Mitigated** (2026-10-15). A password is now used only for directly
> configured OpenCode URLs; a password-protected endpoint found by
> discovery is skipped rather than sent credentials. A healthy-looking
> same-user impostor can still be discovered and receive answers if you act on
> its forged state; checking the process executable remains optional work.
> OpenCode JSON snapshots are capped at 4 MiB per response and 16 MiB total,
> with limits of 1,000 sessions and 30 seconds; SSE event frames are capped at
> 1 MiB. *Verified* by
> `TestDiscoveredOpenCodeDoesNotReceiveConfiguredPassword`,
> `TestExplicitOpenCodeReceivesConfiguredPassword`,
> `TestSnapshotRejectsOversizedJSONResponse`, and
> `TestSnapshotCapsNumberOfSessions`.

**L5. Supply chain.** *Design.*
The bridge would install from GitHub into **every** OpenCode process, and the
Claude plugin runs `amc` from `PATH` on every hook. *Fix:* pin
plugin specifiers to a tag or commit (`github:…#<sha>`); publish SHA-256
checksums (and ideally Sigstore signatures) for release binaries; pin GitHub
Actions by SHA; keep the Go code stdlib-only (current state: zero third-party
Go modules).

**L6. Sensitive text at rest in the journal.** *Design.*
The daemon logs titles, messages and questions to journald (persistent on
most distros). They may quote secrets an agent printed. *Fix:* log IDs and
statuses, not agent text, by default; `-log-text` to opt in.

> **Fixed** (2026-10-15). State changes log `<workflow id> <status>  (rule: …)`
> only; `serve -log-text` adds the (cleaned) message and title. Unattributed
> events log their status, not their text. *Verified* by test
> (`TestChangeLineKeepsAgentTextOutByDefault`) and live in the journal.

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
- **Bounded state and snapshots:** 1,000 workflows per adapter, 2,000 total,
  10,000 parent links, and 1,000 Claude Code adapter sessions; OpenCode JSON
  snapshots are limited to 4 MiB per response, 16 MiB total, 1,000 sessions,
  and 30 seconds; SSE event frames are limited to 1 MiB.
- **Hooks can't harm the agent:** `amc hook` prints nothing,
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

## 7. Remaining work

1. **Upstream:** report OpenCode's missing Host validation (it affects
   `opencode --port` and `opencode serve`).
2. **L4:** optionally verify the executable behind an auto-discovered healthy
   OpenCode endpoint. Password-protected native servers must be configured
   explicitly; discovery never receives the password.
3. **L5:** signatures (e.g. Sigstore) for release binaries. Done: Actions
   pinned by SHA, SHA-256 checksums published with each release, and the
   plugins ship inside the binary (`amc install`), so there is
   no separate plugin specifier to pin.
4. The bridge's token file survives a killed (SIGTERM) OpenCode, since the
   plugin's `dispose` doesn't run. Harmless (the daemon reads it by live
   PID, and a reused PID's new process overwrites it), but the daemon could
   remove files of exited processes.
5. Before any remote or shared view (section 6): authentication, TLS,
   per-workflow authorization, audit log.

## 8. Review of 2026-10-15

Re-verified live: runtime dir 0700, socket and token 0600; TCP without
token, or with a wrong one → 401; forged `Host` → 403; `text/plain` POST →
415; the hook endpoint over TCP without a token → 401; the daemon serves no
HTTP/2 (h2c refused). Bridge: health open, no token → 401, forged `Host` /
`Origin` → 403. MCP still offers only `once`/`reject` for permissions.

| ID | Finding | Severity | Fix, and how it was verified |
|---|---|---|---|
| N1 | `uninstall` deleted files through a symlink: a plugin directory installed as files and later replaced by a symlink into a checkout lost the checkout's files. *Verified* by a probe test. | Bug (data loss) | `Uninstall` skips paths under a symlink, as `Install` does. Test `TestUninstallNeverDeletesThroughASymlink`. |
| N2 | The locally built daemon used Go 1.27.1, affected by 9 standard-library vulnerabilities (GO-2026-6603…6617, mostly HTTP/2, which the daemon does not serve; the textproto header-memory bypass was reachable pre-auth by local processes). The v0.1.0 release was built with 1.27.2. *govulncheck.* | Low | `toolchain go1.27.2` in `go.mod` (older local Go fetches it) and `mise.toml` pinned to 1.27.2. govulncheck: no vulnerabilities. |
| N3 | The bridge resolved the request path against the inner server URL, so `//other.host/x` named another host. Needs the bridge token. *Verified* not exploitable on OpenCode 1.18.33: its in-process fetch ignores the host (a listener on the named host got nothing). | Low (hardening) | The bridge concatenates the path onto the inner origin. Verified live on a fresh `opencode serve`: all bridge controls unchanged. |
| N4 | The token file outlived the daemon, and the UIs kept calling 127.0.0.1:4200 while it was down: on a shared machine, a user binding that port got the (stale) token, could show fake workflows and receive typed answers. *Design.* | Low (shared machines) | The daemon removes its token file on shutdown (only if it is still its own). The GNOME client re-reads the token before every request and sends nothing without one; the widget's FileView drops the token when the file goes (verified) and `Api.request` sends nothing without one. Tests: `TestTokenFileLivesOnlyAsLongAsTheDaemon`, `client.test.js` (a listener on the port saw 0 connections). Live: stop → no token file, start → new one. |
| N5 | No HTTP server timeouts: local processes could hold connections open before authenticating. *Design.* | Low | `ReadHeaderTimeout` 5s, `IdleTimeout` 2m, 64 KB header cap (no write timeout: answers wait on agents). Live: a half-sent request is closed after 5.0s. |
| N6 | Bidi overrides and zero-width characters in agent text could make titles and questions display differently than they read (CLI, GNOME, QML). *Design.* | Low | With L1: `adapter.CleanText` at ingest; GNOME's `clean()` strips the same set. |
| N7 | A same-user process named `opencode` could receive the shared server password through auto-discovery, and OpenCode responses could consume unbounded memory or time. *Design.* | Low | Passwords are limited to directly configured URLs; discovery skips endpoints that require auth. JSON responses are limited to 4 MiB each, snapshots to 16 MiB, 1,000 sessions and 30 seconds, and SSE frames to 1 MiB. Tests: `TestDiscoveredOpenCodeDoesNotReceiveConfiguredPassword`, `TestSnapshotRejectsOversizedJSONResponse`, `TestSnapshotCapsNumberOfSessions`, `TestSubscribeDropsOversizedSSEFrame`. |
| N8 | Forged or never-ended sessions could grow workflow, parent-link and Claude adapter state without bound. *Design.* | Low | Store caps at 1,000 workflows per adapter / 2,000 total and 10,000 parent links; Claude adapter caps at 1,000 sessions and expires idle state/completed workflows after 24 hours. Tests: `TestWorkflowCountIsBounded`, `TestParentLinksAreBounded`, `TestPruneIdleOnlyRemovesCompletedWorkflows`, `TestExpireIdleSessions`, `TestNormalizeBoundsSessionState`. |

Not findings: the transcript tail reader reads to end of file after
seeking (same-user files only).
