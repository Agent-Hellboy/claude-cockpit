# claude-cockpit

A status line + session optimizer for [Claude Code](https://claude.com/claude-code),
shipped as a single dependency-free binary. It keeps your context/token/cost
usage visible so you can `/compact` on your own terms, and quietly analyzes how
the session is going to suggest token-saving moves (cheaper model, plan mode, a
skill/MCP, building a code graph, etc.).

```
mcp-runtime ⎇main ⇡#336 │ Opus 4.8 (1M context) high │ ctx ▓▓▓▓▓▓▓▓▓░ 99% 985k/1.0M ⚠ /compact
+3197/-570 · out 12k · cache 985k │ 5h 95% · 7d 62% │ $24.30
🤖 Switch to Haiku — current work is mechanical (rename, gofmt); doesn't need Opus, saves ~60%
```

## Install

**No Go, no jq, no runtime — just a prebuilt binary.**

```bash
curl -fsSL https://raw.githubusercontent.com/Agent-Hellboy/claude-cockpit/main/install.sh | bash
```

This downloads the right binary for your OS/arch into `~/.claude/bin/cockpit`,
clears the macOS quarantine bit, and self-registers `statusLine` + the `Stop`
hook into `~/.claude/settings.json` (merging, never overwriting — your other
settings and hooks are preserved; a timestamped backup is made).

Then **restart Claude Code** (or run `/hooks`) so the Stop hook loads. The status
bar appears immediately.

> Prefer to build it yourself? `go install github.com/Agent-Hellboy/claude-cockpit/cmd/cockpit@latest && cockpit install`

### Uninstall

```bash
~/.claude/bin/cockpit uninstall
```

Removes only cockpit's entries (foreign hooks and other settings are kept),
deletes transient state, and backs up `settings.json`.

## What you get

**1. Status line** (`cockpit statusline`) — two rows, never truncated:
- **Row 1:** dir + branch (+ PR number/review state), model + effort, and a
  context-fill gauge that turns yellow ≥70%, red ≥90% with a `⚠ /compact` cue.
- **Row 2:** session churn (`+/-` lines), output/cache tokens, 5h/7d rate-limit
  usage, and session cost.
- **Row 3 (only when present):** the latest suggestion from the analyzer.

**2. Session analyzer** (`cockpit analyze`, a `Stop` hook) — after each turn it
gathers cheap signals (turn count, ~context size, tool histogram, repeated reads,
search load, current model, available skills, whether a graphify graph exists,
repo size) and asks a fast model (`haiku`) for the 1–3 highest-leverage
optimizations *right now*. It is **advisory** — it never changes your session;
you act on the suggestion (`/model`, Shift+Tab, `/graphify`, …).

Design notes:
- **Cheap by construction** — signals are gathered in-process (no subprocess
  fan-out); the model only sees a compact summary, throttled by an auto-scaling
  cadence.
- **Auto-scales** — short sessions are analyzed rarely (every 10th turn), long
  sessions almost every turn, so analysis surfaces on its own as you go.
- **No-graph aware** — if there's no graphify graph and you're searching a lot, it
  offers to build one with a repo-size-scaled ETA instead of suggesting a query
  that can't run.
- **Non-blocking** — the analysis runs in a fully detached background process, so
  your turn never waits on it. Results land in `~/.claude/.session-report` and
  the status bar.

## Requirements

- The `claude` CLI on your `PATH` (the analyzer shells out to it for suggestions;
  the status line works without it).
- `curl` + `tar` to run the installer.

## How it works

The analyzer writes its top suggestion to `~/.claude/.model-hint` and the full
list to `~/.claude/.session-report`. The status line reads `.model-hint` to show
row 3. A `MODEL_HINT_GUARD` env var stops the background `claude -p` call from
re-triggering the hook.

## Subcommands

| Command | Purpose |
|---|---|
| `cockpit statusline` | render the status bar (stdin = Claude Code's status JSON) |
| `cockpit analyze` | the `Stop` hook (stdin = hook JSON) |
| `cockpit install` / `uninstall` | register/unregister in settings.json |
| `cockpit version` | print version |
| `cockpit worker FILE` | internal: detached background classifier |

## Build & test (contributors)

```bash
go build ./... && go test ./... -race
```

Releases are cut by tagging: `git tag v0.1.0 && git push --tags` triggers the
GitHub Actions `release` workflow (goreleaser) to build and publish darwin/linux
× amd64/arm64 binaries.

## Caveats

- Hooks are **advisory** in Claude Code — they can suggest, not switch models or
  enter plan mode. You act on the suggestion.
- The analyzer infers your *current* model from the transcript, so an occasional
  already-on-Sonnet suggestion is harmless.

## License

MIT — see [LICENSE](LICENSE).
