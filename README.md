# Agent Flightdeck

Live instruments and control suggestions for long-running coding-agent sessions.

Agent Flightdeck installs the `cockpit` command. In Claude Code it adds a compact
status line and `Stop` hook that suggest the next useful control before a session
gets expensive, repetitive, or hard to steer. Across Claude Code, Codex, and
Cursor it discovers agent-specific project surfaces and can propagate accepted
rules or skills into the right files.

![Agent Flightdeck status line](docs/statusline.png)

## Why use it

- See branch, PR state, model, effort, context pressure, token churn, rate-limit
  usage, and session cost while you work.
- Get timely suggestions for `/compact`, `/clear`, model changes, skills,
  subagents, MCP, graphify, and workflow tools.
- Accept a suggestion in one step: numbered rows in the status bar map to
  `cockpit apply <n>`, which updates project config only after you confirm.
- Keep Claude Code, Codex, and Cursor project guidance aligned from the same
  accepted control.
- Save compact hourly memory of coding-agent sessions for retrieval by other
  tools without storing raw transcripts.

## Agent support

| Agent | Support |
|---|---|
| Claude Code | Live status line, `Stop`/`SessionEnd` hooks, `/cockpit`, MCP discovery, skills, subagents |
| Codex | `cockpit install codex`, `AGENTS.md` pointer to the shared Agent Flightdeck skill, project/user skill and agent discovery, shared `.mcp.json` discovery |
| Cursor | `cockpit install cursor`, shared Agent Flightdeck skill discovery, Cursor MCP config discovery |

Claude Code is currently the live instrumentation source because it exposes the
status-line and hook payloads this tool reads. Codex and Cursor support is
file-based today.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/Agent-Hellboy/agent-flightdeck/main/install.sh | bash
```

The installer auto-detects installed/configured coding agents and registers
Agent Flightdeck for the ones it finds.

The installer downloads the matching macOS or Linux release binary, installs it
to `~/.claude/bin/cockpit`, then runs `cockpit install`. For Claude Code it
merges the `statusLine` plus hooks into `~/.claude/settings.json`; for Codex it
writes an `AGENTS.md` pointer; for Cursor it writes the shared Agent Flightdeck
skill. Existing Claude settings and hooks are preserved, with a timestamped
backup. Restart Claude Code, or run `/hooks`, so Claude hooks load.

Build from source:

```bash
go install github.com/Agent-Hellboy/agent-flightdeck/cmd/cockpit@latest
cockpit install
```

`cockpit install` auto-detects present agents. To force a specific target:

```bash
cockpit install codex
cockpit install cursor
cockpit install all
```

## What it shows

![multiple suggestions](docs/bar-suggestions.png)

- **Status line:** project, git state, model, effort, context fill, token churn,
  cache/output tokens, rate limits, and cost.
- **Session advisor:** a background `haiku` check that surfaces the highest-value
  next controls for the current session.
- **Session memory:** the daemon scans changed Claude/Codex/Cursor session files
  hourly and writes compact JSONL summaries of what the user asked, when it
  happened, which tools ran, and which files were touched.
- **Tool awareness:** suggestions can reference Claude Code commands, shared
  Agent Flightdeck skills, installed Claude/Codex skills and agents, MCP
  resources, graphify state, and audited third-party tool gaps.
- **Non-blocking runtime:** analysis runs detached, so your turn does not wait on
  the advisor.

More examples:

![graphify build suggestion](docs/bar-graphify.png)
![model downgrade suggestion](docs/bar-model.png)
![skill suggestion](docs/bar-skill.png)

## Accepting suggestions

After a few turns, the advisor writes suggestions to the status bar.
**Notes** are informational only. **Numbered fixes** can be wired into your
project with `cockpit apply`:

```bash
cockpit list
cockpit apply 1
cockpit apply 2 --yes
cockpit apply 3 --dry-run
```

When you accept a fix, Agent Flightdeck may:

- Append the accepted rule to `.agent-flightdeck/skills/agent-flightdeck/SKILL.md`.
- Merge MCP servers into `.mcp.json`.
- Write project skills to `.agent-flightdeck/skills/<name>/SKILL.md`.
- Run safelisted install commands such as `brew`, `npm`, or `npx -y`.

Restart Claude Code or run `/hooks` after MCP servers are added so they load.

## Background memory

The daemon runs a memory scan once an hour. It does not persist raw transcripts;
it stores compact records in `~/.claude/cockpit-logs/memory.jsonl` and tracks
processed files in `memory-state.json` so unchanged sessions are skipped.

```bash
cockpit memory             # latest compact summaries
cockpit memory --json      # JSONL for another system to consume
cockpit memory --scan      # scan now, then print memory
cockpit memory auth --json # query summaries by text
```

## Commands

| Command | Purpose |
|---|---|
| `cockpit install` | Auto-detect present coding agents and register their integrations |
| `cockpit install codex` | Add an `AGENTS.md` pointer and the shared Agent Flightdeck skill |
| `cockpit install cursor` | Add the shared Agent Flightdeck skill, no Cursor rule |
| `cockpit install all` | Install Claude Code hooks plus the shared project skill |
| `cockpit uninstall` | Remove Claude Code cockpit settings and transient state |
| `cockpit uninstall codex` | Remove the managed Codex pointer |
| `cockpit uninstall cursor` | No-op for Cursor-specific files; Cursor uses the shared skill |
| `cockpit statusline` | Render the cockpit status line for Claude Code, Codex, Cursor, or another agent |
| `cockpit analyze` | Run the `Stop` hook analyzer |
| `cockpit list` | Show numbered suggestions |
| `cockpit apply N` | Accept suggestion N - updates agent instructions, MCP, skills |
| `cockpit systems` | Synoptic view of hooks, agents, MCP, skills, graphify |
| `cockpit checklist <topic>` | Procedure for `context`, `budget`, `search`, and related topics |
| `cockpit plan` | Session route, cost index, deviation |
| `cockpit status` | Deferred items |
| `cockpit debrief [session]` | Post-session summary |
| `cockpit memory [query]` | Retrieve compact background session memory |
| `cockpit memory --json` | Emit memory as JSONL for other systems |
| `cockpit memory --scan` | Scan sessions immediately before retrieval |
| `cockpit daemon start` | Start persistent advisor daemon |
| `cockpit daemon stop` | Stop advisor daemon |
| `cockpit daemon status` | Show daemon state and queue depth |
| `cockpit version` | Print the installed version |

## Controls

| Variable | Effect |
|---|---|
| `COCKPIT_ANALYZE_DISABLE=1` | Disable advisor analysis; keep the status line |
| `COCKPIT_ANALYZE_PROMPTS=0` | Omit recent prompt text from analyzer signals |
| `COCKPIT_DEBUG=1` | Write debug logs to `~/.claude/cockpit-logs/.cockpit-debug.log` |
| `COCKPIT_DISPLAY` | `minimal`, `full` (default), or `debug` |
| `COCKPIT_COST_INDEX` | `eco`, `normal` (default), or `perf` |
| `COCKPIT_ALERT_CHIME=1` | Terminal bell when context crosses 90% |
| `COCKPIT_MEMORY_DISABLE=1` | Disable hourly background memory scans |
| `COCKPIT_MEMORY_INTERVAL` | Override scan interval, e.g. `30m` or `3600` |
| `COCKPIT_CLAUDE_SESSION_DIR` | Override Claude transcript scan root |
| `COCKPIT_CODEX_SESSION_DIR` | Override Codex session scan root |
| `COCKPIT_CURSOR_SESSION_DIR` | Override Cursor session scan root |
| `CLAUDE_CONFIG_DIR` | Use a different Claude config directory |
| `CODEX_HOME` | Use a different Codex config directory |
| `CURSOR_CONFIG_DIR` | Use a different Cursor config directory |
| `COCKPIT_VERSION` | Pin installer downloads to a release tag |

## Develop

```bash
go build ./...
go test ./... -race
```

Release by pushing a tag such as `v0.1.0`; GitHub Actions builds the prebuilt
macOS and Linux binaries.

## License

MIT. See [LICENSE](LICENSE).
