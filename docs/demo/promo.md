# Promo: clip and posts

Drafts for sharing agent-mission-control with the Omarchy community. Nothing
here has been posted.

## The clip

`docs/demo/record-promo.sh` renders `promo.mp4` (1920x1080, H.264, about 29s,
no audio; for X) and `promo.gif` (960 wide, for Discord and GitHub). It runs
completely offscreen against a throwaway daemon, so it is safe to run on the
desktop you are using (see the script header). The mp4 is not committed
(`.gitignore`); re-run the script to get it.

Shot list (seconds):

| t | What you see |
|---|---|
| 0–3 | Title card over the desktop: "Which agent is waiting on me?" |
| 3–6 | Three terminals working: two OpenCode, one Claude Code. The bar icon spins with a count of 3 |
| 6–7 | The auth agent asks which session store to use. The icon turns amber, pulses, count 1, and a notification pops up. The camera zooms to the bar |
| 9–11 | The cursor clicks the icon. The panel lists the question with its options |
| 12–13 | Click "Redis". The panel says "answered: Redis", the agent resumes |
| 13–17 | Claude Code finishes, the flaky-test agent finishes, the auth agent finishes. Every card ends up done |
| 18–19 | The panel closes, zoom out. The icon is plain: no count, no spinner |
| 21–29 | End card: name, one-liner, repo URL, and a footnote that the take is scripted |

What is real and what is staged: the `amc` daemon, the omarchy-shell widget
(loaded from `ui/omarchy` as omarchy-shell loads it), its panel, its
notification text and the answer that reaches the agent are real. The agents
are the scripted fake OpenCode server (`fake-opencode -story promo`) and a
Claude Code session replayed through the real `amc hook claude-code`. The
desktop (bar chrome, terminals, cursor, toast styling, captions) is drawn by
`promo/shell.qml`. The urgent color is the theme's; the scene uses amber.

## X

> Which agent is waiting on me?
>
> Three coding agents in three terminals, and one has been sitting on a question for 20 minutes. Now my Omarchy bar pulses when an agent needs me, and I answer it right from the panel.
>
> OpenCode + Claude Code, open source:
> github.com/rgolangh/agent-mission-control

275 characters as X counts them (a link counts as 23). Attach `promo.mp4`. Optional: tag @dhh in a reply rather than the post
itself. Alt text: "Screen recording of an Omarchy desktop with three coding
agents working. One asks a question, the bar icon turns amber and pulses, the
user opens the panel from the bar, clicks Redis, and all three agents finish."

## Discord (Omarchy, #showcase)

> **Agent Mission Control: which coding agent is waiting on you, in your bar**
>
> I run a few coding agents at once (OpenCode, Claude Code) and kept losing track of which tab had stopped to ask me something. So I built a small daemon plus an omarchy-shell bar widget:
>
> • the icon counts agents that need you, and pulses (with a notification) when one asks a question or wants a permission
> • click it for a panel with every session: running, waiting, stuck, done
> • answer questions and approve permissions from the panel (OpenCode), or jump to the agent's terminal
> • done vs stuck vs "asked in plain text" is worked out by rules, no LLM
>
> Install (Arch/Omarchy):
> ```
> curl -LO https://github.com/rgolangh/agent-mission-control/releases/download/v0.1.2/agent-mission-control-bin-0.1.2-1-x86_64.pkg.tar.zst
> sudo pacman -U ./agent-mission-control-bin-0.1.2-1-x86_64.pkg.tar.zst
> amc install --enable
> ```
> Repo: https://github.com/rgolangh/agent-mission-control
>
> Early days. Feedback on misclassified sessions and install friction is very welcome.

Attach `promo.gif` (or the mp4).

## GitHub Discussions (Show and tell)

Title: **Agent Mission Control: see which coding agent is waiting on you, from the Omarchy bar**

> ![Agent Mission Control in the Omarchy bar](https://github.com/rgolangh/agent-mission-control/raw/main/docs/demo/promo.gif)
>
> **The problem:** a few coding agents running in different terminals, and the one that stopped to ask you something sits there unnoticed.
>
> **What it does:** a small daemon (`amc`) follows every OpenCode and Claude Code session on your machine and works out whether each one is running, waiting on you, stuck, or done. An omarchy-shell bar widget shows the count of agents that need you, pulses and notifies when one asks something, and opens a panel where you can answer questions and approve permissions without finding the right tab. The same state is available from the CLI (`amc status`, `amc answer`) and over MCP, so an orchestrating agent can answer for you.
>
> **How it decides "done" vs "stuck" vs "waiting":** ordered rules over how the turn ended (errors, finish reason, a closing question, "I couldn't…" blockers, open todos). No LLM, and every verdict quotes its evidence.
>
> **Install**
> ```bash
> curl -LO https://github.com/rgolangh/agent-mission-control/releases/download/v0.1.2/agent-mission-control-bin-0.1.2-1-x86_64.pkg.tar.zst
> sudo pacman -U ./agent-mission-control-bin-0.1.2-1-x86_64.pkg.tar.zst
> amc install --enable   # service, agent integrations, bar widget
> ```
>
> **Security:** it can approve agent permissions, so the API is a user-only Unix socket plus a per-start bearer token on the loopback port the widget uses. Agent text is rendered as plain text and never reaches a shell. Details in SECURITY.md.
>
> **Status:** early. Tested with OpenCode 1.18 and Claude Code 2.1 on Linux. Claude Code sessions are observe-only (answer in the terminal; it has no input API). Next up: GitHub PR and Jira sources linked to the sessions that produced them.
>
> The clip is a scripted take (fake agents, real daemon and widget) so it is reproducible: `docs/demo/record-promo.sh`.
>
> Feedback welcome, especially sessions it gets wrong.

## Before posting (TODO-verify)

- [ ] The `curl -LO` URL and `pacman -U` file name point at the release being announced. Once the AUR package exists, switch to `yay -S agent-mission-control-bin`.
- [ ] Whether the package (or `amc install`) also sets up the OpenCode bridge and Claude Code hooks, or the posts need one more line.
- [ ] The Discussions GIF link points at `main`: merge `docs/demo/promo.gif` first, or upload the GIF into the post instead.
- [ ] "20 minutes" in the X post is illustrative.
