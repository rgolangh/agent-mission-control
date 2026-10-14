# mission-control for Claude Code

A Claude Code plugin with no logic in it: each hook runs `mission-control hook
claude-code`, which posts the hook's payload to the mission-control daemon and
exits 0 without printing anything, so it can never block or steer Claude. If
the daemon isn't running, the hook returns at once (400ms timeout at most). The daemon
(`internal/claudecode`) does the rest: state per session, the question
Claude asked you, and done/stuck judgment from `Stop`'s
`last_assistant_message` plus the transcript.

Claude Code sessions are **observe-only** in mission-control. There is no API
to type into a Claude Code session, so you answer in the terminal (the
dashboard can focus it).

## Install (no marketplace needed: a "skills-dir" plugin)

```bash
ln -s ~/Projects/mission-control/integrations/claude-code ~/.claude/skills/mission-control
```

It loads in new sessions as `mission-control@skills-dir` (or `/reload-plugins`
in a running one). `mission-control` must be on `PATH` (`~/.local/bin`).

Turn it off: `claude plugin disable mission-control@skills-dir`. Remove: delete
the symlink.
