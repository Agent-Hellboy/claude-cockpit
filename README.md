# claude-cockpit

A status line + session optimizer for [Claude Code](https://claude.com/claude-code).
Keeps your context/token/cost usage visible so you can `/compact` on your own
terms, and quietly analyzes how the session is going to suggest token-saving
moves (cheaper model, plan mode, a skill/MCP, building a code graph, etc.).

```
mcp-runtime ⎇main ⇡#336 │ Opus 4.8 (1M context) high │ ctx ▓▓▓▓▓▓▓▓▓░ 99% 985k/1.0M ⚠ /compact
+3197/-570 · out 12k · cache 985k │ 5h 95% · 7d 62% │ $24.30
🔄 Switch to /model sonnet — recent prompts are mechanical; Sonnet handles this, saves ~50% tokens
```

## What you get

**1. Status line** (`statusline.sh`) — two rows, never truncated:
- **Row 1:** dir + branch (+ PR number/review state), model + effort, and a
  context-fill gauge that turns yellow ≥70%, red ≥90% with a `⚠ /compact` cue.
- **Row 2:** session churn (`+/-` lines), output/cache tokens, 5h/7d rate-limit
  usage, and session cost.
- **Row 3 (only when present):** the latest suggestion from the analyzer.

**2. Session analyzer** (`hooks/session-analyzer.sh`, a `Stop` hook) — after each
turn it gathers cheap signals (turn count, ~context size, tool histogram,
repeated reads, search load, current model, available skills, whether a graphify
graph exists, repo size) and asks a fast/cheap model (`haiku`) for the 1–3
highest-leverage optimizations *right now*. It is **advisory** — it never changes
your session; you act on the suggestion.

Design notes:
- **Cheap by construction** — signals are gathered token-free with `jq`; the model
  only sees a compact summary, throttled by an auto-scaling cadence.
- **Auto-scales** — short sessions are analyzed rarely (every 10th turn), long
  sessions almost every turn, so analysis surfaces on its own as you go.
- **No-graph aware** — if there's no graphify graph and you're searching a lot, it
  offers to build one with a repo-size-scaled ETA instead of suggesting a query
  that can't run.

## Requirements

- `jq` (required)
- The `claude` CLI on your `PATH` (optional — the status line works without it;
  the analyzer's AI suggestions stay off until it's present)
- Claude Code with `statusLine` + `hooks` support

## Install

```bash
git clone <your-fork-url> claude-cockpit
cd claude-cockpit
./install.sh
```

The installer copies the scripts into `~/.claude/`, backs up your
`settings.json`, and **merges** in the `statusLine` and `Stop` hook with `jq`
(your other settings are untouched). It's idempotent — re-run it to update.

Then **restart Claude Code** (or run `/hooks`) so the Stop hook loads. The status
bar appears immediately.

> Honors `CLAUDE_CONFIG_DIR` if you keep your config somewhere other than
> `~/.claude`.

## Uninstall

```bash
./uninstall.sh
```

Removes only the cockpit entries (foreign hooks and other settings are kept),
deletes the installed scripts and transient state, and backs up `settings.json`.

## How it works

The analyzer writes its top suggestion to `~/.claude/.model-hint` and the full
list to `~/.claude/.session-report`. The status line reads `.model-hint` to show
row 3. A shared `MODEL_HINT_GUARD` env var stops the background `claude -p` call
from re-triggering the hook.

## Tuning

Both scripts are short and commented; edit them in `~/.claude/`:

- **Analysis cadence** — `session-analyzer.sh`, the `K=10 / 5 / 2` tiers and their
  turn thresholds (`N < 10`, `N < 25`).
- **Graph-build ETA bands** — the file-count → time mapping in `session-analyzer.sh`.
- **Context gauge thresholds** — `statusline.sh`, the `70` / `90` percentages.
- **Classifier model** — change `--model haiku` if you prefer another.

Re-run `./install.sh` from the repo to push edits back, or just edit the copies
in `~/.claude/` directly.

## Caveats

- Hooks are **advisory** in Claude Code — they can suggest, not switch models or
  enter plan mode. You act via `/model`, Shift+Tab, `/graphify`, etc.
- The analyzer can't see your *current* model with certainty (it infers it from
  the transcript), so an occasional already-on-Sonnet suggestion is harmless.
- macOS ships bash 3.2; the scripts are written to stay compatible.

## License

MIT — see [LICENSE](LICENSE).
