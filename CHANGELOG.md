# Changelog

All notable changes to claude-cockpit are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The section for each released version is used verbatim as the GitHub release
notes, so keep entries user-facing and concise. Add new work under
`[Unreleased]`; on release, rename it to the version with a date.

## [Unreleased]

### Fixed
- **Suggestions leaked across concurrent sessions.** The advisor stored its
  report, snapshot, and chime state in single global files, so the daemon's
  last-processed session pushed its suggestions into every other session's
  status bar, `cockpit list`, and `apply` — you could see (and apply!) advice
  computed from a different project's transcript. All advisor state is now
  per-session (`<session>.report` / `.snapshot` / `.chime`, stamped with the
  session id and project directory). Terminal commands resolve their session
  by project directory (`COCKPIT_SESSION` overrides); with several live
  sessions, a directory that matches none shows nothing rather than someone
  else's suggestions. The statusline scopes by the payload's `session_id`,
  context-pressure classification uses the session's own instruments, `apply`
  reads the session's own signals (not the newest file from any session), and
  SessionEnd cleanup removes only that session's artifacts. `cockpit list`
  now names the session it is scoped to.

- **`/cockpit apply <n>` (and any multi-word argument) failed with
  `unknown subcommand "apply 1"`.** The slash command re-expanded the
  substituted `$ARGUMENTS` from a shell variable, and an unquoted `${VAR}`
  word-splits in bash but *not* in zsh (the macOS default shell), so `apply 1`
  was passed to the binary as a single argument. The `/cockpit` command now
  routes arguments through an inner `sh -c` as positional parameters, which
  split identically in every POSIX shell. A bare `/cockpit` still defaults to
  the `systems` synoptic. Re-run `cockpit install` to pick up the fix.

## [0.1.7] - 2026-07-02

### Fixed
- **Advisor never fired in short sessions.** The Stop-hook analyzer only ran on
  `turn % 10 == 0`, and the per-session turn counter resets every session — so
  a session under 10 turns never got a suggestion. It now fires on turns 1-3
  and then throttles by cadence. Added `COCKPIT_ANALYZE_CADENCE` to override
  the throttle explicitly.
- **5h/7d rate limits showed 0% on a fresh session.** The status bar only read
  `rate_limits` from the live payload, ignoring the persisted state — so a
  render where that block was momentarily absent showed 0% instead of the
  last-known value. It now falls back to the stored 5h/7d. Relatedly,
  `SessionEnd` was deleting `.cockpit-state`/`.cockpit-snapshot` (the
  cross-session rate/context memory) on every session end; those now survive,
  and `writeState` persists rate-limit data even when the context window size
  is momentarily 0.
- **Installer left a stale advisor daemon running after an upgrade.**
  `install.sh` now stops any running daemon before replacing the binary (a
  running daemon holds the old binary's code in memory) and verifies the
  downloaded tarball against `checksums.txt`.

### Changed
- All cockpit state, logs, and job files now live under
  `~/.claude/cockpit-logs/` instead of loose dotfiles in `~/.claude/`.

## [0.1.6] - 2026-07-01

### Added
- **`/cockpit` slash command** to manage cockpit from inside Claude Code. Bare
  `/cockpit` shows the systems synoptic; arguments pass straight through —
  `status`, `list`, `apply <n>`, `checklist <topic>`, `plan`, `debrief`,
  `daemon status`. `cockpit install` writes it to
  `~/.claude/commands/cockpit.md` (with the absolute binary path baked in so it
  works under a custom `CLAUDE_CONFIG_DIR`); `cockpit uninstall` removes it.
- **Named advisor section.** Suggestions now sit under a titled
  `▸ advisor on · N controls` header on its own line, instead of trailing off
  the end of the metrics row.
- **Session phase badge** (`● steady / review / ship / hot / warmup`) leads the
  top row, showing where the session is at a glance.
- **Severity chips.** Warning and caution suggestions render as filled
  reverse-video chips so urgent controls stand out; advisories and memos stay
  quiet.

### Changed
- **Width-aware status bar.** Instrument segments now drop by priority to fit the
  terminal width, with location, context %, and cost always pinned. Rows no
  longer overflow and wrap into the suggestion lines on narrow terminals.
- The phase badge leads with a plain gap (`● steady  repo …`) rather than a `·`
  separator, so it reads as a mode header rather than a peer field.

### Fixed
- The metrics row could soft-wrap on narrow terminals and visually collide with
  the advisor suggestions rendered below it.

## [0.1.5] and earlier

See the [GitHub releases](https://github.com/Agent-Hellboy/claude-cockpit/releases)
page for notes on releases prior to the introduction of this changelog.

[Unreleased]: https://github.com/Agent-Hellboy/claude-cockpit/compare/v0.1.7...HEAD
[0.1.7]: https://github.com/Agent-Hellboy/claude-cockpit/compare/v0.1.6...v0.1.7
[0.1.6]: https://github.com/Agent-Hellboy/claude-cockpit/compare/v0.1.5...v0.1.6
