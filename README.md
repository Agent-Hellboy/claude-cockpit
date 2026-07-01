# claude-cockpit

Live instruments and control suggestions for long-running Claude Code sessions.

`claude-cockpit` adds a compact status line to Claude Code and uses the `Stop`
hook to suggest the next useful control before a session gets expensive,
repetitive, or hard to steer. Suggestions are advisory — you choose whether to
pull the lever. When you accept one, `cockpit apply` can wire it into your
project (`CLAUDE.md`, MCP, skills) after you confirm.

![claude-cockpit status line](docs/statusline.png)

## Why use it

- See branch, PR state, model, effort, context pressure, token churn, rate-limit
  usage, and session cost while you work.
- Get timely suggestions for `/compact`, `/clear`, model changes, skills,
  subagents, MCP, graphify, or workflow tools.
- Accept a suggestion in one step: numbered rows in the status bar map to
  `cockpit apply <n>`, which updates project config only after you say yes.
- Install one small binary with no Go, jq, or runtime dependency.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/Agent-Hellboy/claude-cockpit/main/install.sh | bash
```

Then restart Claude Code, or run `/hooks`, so the hook loads.

The installer downloads the matching macOS or Linux release binary, installs it
to `~/.claude/bin/cockpit`, and merges the `statusLine` plus `Stop` hook into
`~/.claude/settings.json`. Existing Claude settings and hooks are preserved, with
a timestamped backup.

To install a specific release:

```bash
curl -fsSL https://raw.githubusercontent.com/Agent-Hellboy/claude-cockpit/main/install.sh | COCKPIT_VERSION=v0.1.0 bash
```

Build from source:

```bash
go install github.com/Agent-Hellboy/claude-cockpit/cmd/cockpit@latest
cockpit install
```

## What it shows

![multiple suggestions](docs/bar-suggestions.png)

- **Status line:** project, git state, model, effort, context fill, token churn,
  cache/output tokens, rate limits, and cost.
- **Session advisor:** a background `haiku` check that surfaces the highest-value
  next controls for the current session.
- **Tool awareness:** suggestions can reference Claude Code commands, installed
  skills, subagents, MCP resources, graphify state, and audited third-party tool
  gaps.
- **Non-blocking runtime:** analysis runs detached, so your turn does not wait on
  the advisor.

More examples:

![graphify build suggestion](docs/bar-graphify.png)
![model downgrade suggestion](docs/bar-model.png)
![skill suggestion](docs/bar-skill.png)

## Accepting suggestions (`cockpit apply`)

After a few turns, the advisor writes suggestions to the status bar.
**Notes** (efficiency memos, slash-command reminders) are informational only.
**Numbered fixes** can be wired into your project with `cockpit apply`:

```
● steady · repo ⎇main · Sonnet · ctx ██░░ 32%
+120/-40 · 5h 12% · $4.20 · advisor on
  note  ✅ session looks efficient.
  1 tip  🔌 Audit Playwright MCP for live browser control...
     apply · cockpit apply 1
```

### List what's available

```bash
cockpit list
```

### Apply a suggestion

```bash
cockpit apply 1          # accept suggestion [1]
cockpit apply 2 --yes    # skip the confirmation prompt
cockpit apply 3 --dry-run  # preview the plan only
```

You can also ask Claude in your session: *"run `cockpit apply 1`"* — same command.

### What happens when you accept

1. Cockpit reads the suggestion and your recent session signals.
2. A focused `haiku` pass turns it into a concrete plan.
3. You see every change and get `Apply this fix? [y/N]:` (unless `--yes`).
4. On confirm, cockpit may:
   - **Append a rule to `CLAUDE.md`** — workflow levers like graphify, `/loop`,
     Explore subagent, model tiering (idempotent; won't duplicate the same rule).
   - **Merge MCP servers into `.mcp.json`** — when the suggestion needs an
     integration (e.g. Playwright, GitHub, Postgres).
   - **Write a project skill** — `.claude/skills/<name>/SKILL.md` when a skill
     should exist locally.
   - **Run safe install commands** — `brew`, `npm`, `npx -y`, etc. (no secrets,
     no destructive flags).
5. The applied suggestion is removed from the status bar.

Restart Claude Code or run `/hooks` after MCP servers are added so they load.

### Flags

| Flag | Effect |
|---|---|
| `--yes` | Apply without prompting |
| `--dry-run` | Show the plan, make no changes |
| `--cwd DIR` | Target a project directory (default: current dir) |

## The cockpit metaphor

Real aircraft cockpits do not learn pilot preferences. They give every operator the
same **instruments**, surface **warnings** for the current flight condition (like
ECAM/EICAS), and expose **controls** the pilot pulls deliberately.

claude-cockpit works the same way:

| Real cockpit | claude-cockpit |
|---|---|
| Gauges (altitude, airspeed, fuel) | Context fill, model, cost, rate limits |
| Phase-aware warnings | Advisor reads live session signals each turn |
| Standardized controls | `/compact`, `/model`, graphify, MCP, subagents |
| Pilot pulls the lever | You choose — or `cockpit apply <n>` after confirming |

Nothing auto-adapts to your history. The bar shows instruments and the next
control worth considering **right now**; you decide whether to act.

### ECAM/EICAS features

| Aviation | claude-cockpit |
|---|---|
| Alert levels (WARN / CAUT / ADV / MEMO) | Color-coded suggestion rows |
| Flight phase (PREFLIGHT → LANDING) | `session_phase` in advisor signals + status bar |
| MEMO line | Dim row: phase, graphify, cost index, pending suggestions |
| ECAM checklist | `cockpit checklist <topic>` |
| System synoptic | `cockpit systems` |
| FMS flight plan | `cockpit plan` |
| STATUS page | `cockpit status` |
| Black-box debrief | `cockpit debrief` (+ auto on session end in logs) |
| Reversionary mode | Rule-based hints if haiku advisor fails |
| Attention getter | `COCKPIT_ALERT_CHIME=1` — terminal bell when ctx crosses 90% |
| Advisor daemon | Persistent ECAM computer — `cockpit daemon start` |

### Avionics computers (how it maps)

Real glass cockpits run **multiple always-on computers**, not one-shot scripts:

| Aircraft LRU | claude-cockpit |
|---|---|
| Data acquisition | `statusline` hook + daemon acquisition loop (refreshes snapshot) |
| Display management (EFIS) | `statusline` render |
| ECAM / EICAS alerting | **Advisor daemon** — queues AI analysis jobs |
| Flight recorder | `cockpit-logs/` + debrief |

On `cockpit install`, the **advisor daemon** starts automatically. The Stop hook
enqueues analysis jobs; the daemon runs them with haiku without spawning a new
process every turn. If the daemon is stopped, cockpit falls back to one-shot workers.

```bash
cockpit daemon status   # is the advisor LRU running?
cockpit daemon start    # start it
cockpit daemon stop     # stop it
```

The memo row shows `advisor on` or `advisor off`.

### Display and FMS controls

| Variable | Effect |
|---|---|
| `COCKPIT_DISPLAY=minimal` | Row 1 instruments only |
| `COCKPIT_DISPLAY=full` | Default — instruments + memo + suggestions |
| `COCKPIT_DISPLAY=debug` | Full + tool histogram row |
| `COCKPIT_COST_INDEX=eco\|normal\|perf` | FMS cost index — biases model suggestions |
| `COCKPIT_ALERT_CHIME=1` | Bell when context hits WARN threshold |

## Commands

| Command | Purpose |
|---|---|
| `cockpit install` | Register the status line and `Stop` hook |
| `cockpit uninstall` | Remove cockpit settings and transient state |
| `cockpit statusline` | Render the Claude Code status line |
| `cockpit analyze` | Run the `Stop` hook analyzer |
| `cockpit list` | Show numbered suggestions |
| `cockpit apply N` | Accept suggestion N — updates `CLAUDE.md`, MCP, skills (with confirmation) |
| `cockpit apply N --dry-run` | Preview what `apply` would do |
| `cockpit systems` | ECAM synoptic — hooks, MCP, skills, graphify |
| `cockpit checklist <topic>` | ECAM procedure (`context`, `budget`, `search`, …) |
| `cockpit plan` | FMS session route, cost index, deviation |
| `cockpit status` | ECAM STATUS — deferred items |
| `cockpit debrief [session]` | Post-session black-box summary |
| `cockpit daemon start` | Start persistent advisor daemon |
| `cockpit daemon stop` | Stop advisor daemon |
| `cockpit daemon status` | Show daemon state and queue depth |
| `cockpit version` | Print the installed version |

Uninstall:

```bash
~/.claude/bin/cockpit uninstall
```

## Privacy and controls

Cockpit writes session state under `~/.claude/` and sends compact session signals
only through your own `claude -p --model haiku` invocation. Web search is used
only when the advisor detects a tool gap that needs current external discovery.

Useful environment variables:

| Variable | Effect |
|---|---|
| `COCKPIT_ANALYZE_DISABLE=1` | Disable advisor analysis; keep the status line |
| `COCKPIT_ANALYZE_PROMPTS=0` | Omit recent prompt text from analyzer signals |
| `COCKPIT_DEBUG=1` | Write debug logs to `~/.claude/.cockpit-debug.log` |
| `COCKPIT_DISPLAY` | `minimal`, `full` (default), or `debug` |
| `COCKPIT_COST_INDEX` | `eco`, `normal` (default), or `perf` — FMS cost bias |
| `COCKPIT_ALERT_CHIME=1` | Terminal bell when context crosses 90% |
| `CLAUDE_CONFIG_DIR` | Use a different Claude config directory |
| `COCKPIT_VERSION` | Pin installer downloads to a release tag |

## Requirements

- Claude Code installed.
- `curl` and `tar` for the installer.
- macOS or Linux on amd64 or arm64 for prebuilt binaries.

## Develop

```bash
go build ./...
go test ./... -race
```

Release by pushing a tag such as `v0.1.0`; GitHub Actions builds the prebuilt
macOS and Linux binaries.

## License

MIT. See [LICENSE](LICENSE).
