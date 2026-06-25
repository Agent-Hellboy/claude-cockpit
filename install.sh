#!/usr/bin/env bash
# claude-cockpit installer.
# Installs a two-row status line (context/token/cost gauge) and a Stop-hook
# session analyzer (token-saving suggestions) into your Claude Code config.
# Safe & idempotent: backs up settings.json and merges with jq instead of
# overwriting. Re-running just refreshes the scripts and entries.
set -euo pipefail

SRC="$(cd "$(dirname "$0")" && pwd)"
CLAUDE_DIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
SETTINGS="$CLAUDE_DIR/settings.json"
STATUSLINE_DST="$CLAUDE_DIR/statusline.sh"
HOOK_DST="$CLAUDE_DIR/hooks/session-analyzer.sh"

say()  { printf '\033[36m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[33m!  \033[0m %s\n' "$1"; }
die()  { printf '\033[31mx  \033[0m %s\n' "$1" >&2; exit 1; }

command -v jq >/dev/null 2>&1 || die "jq is required (brew install jq / apt-get install jq)."
command -v claude >/dev/null 2>&1 || warn "claude CLI not found — the status line still works; the analyzer's AI suggestions stay off until it's installed."

say "Installing scripts into $CLAUDE_DIR"
mkdir -p "$CLAUDE_DIR/hooks"
install -m 0755 "$SRC/statusline/statusline.sh" "$STATUSLINE_DST"
install -m 0755 "$SRC/hooks/session-analyzer.sh" "$HOOK_DST"

# Seed settings.json if missing.
[ -f "$SETTINGS" ] || { echo '{}' > "$SETTINGS"; say "Created $SETTINGS"; }
jq -e . "$SETTINGS" >/dev/null 2>&1 || die "$SETTINGS is not valid JSON — fix or move it, then re-run."

BACKUP="$SETTINGS.bak.$(date +%Y%m%d%H%M%S)"
cp "$SETTINGS" "$BACKUP"
say "Backed up settings.json -> $BACKUP"

# Merge: set our statusLine + add our Stop hook (replacing any prior cockpit
# entry by command path so re-runs don't duplicate). Other keys are untouched.
TMP="$(mktemp)"
jq --arg sl "$STATUSLINE_DST" --arg hook "$HOOK_DST" '
  .statusLine = { type: "command", command: $sl, padding: 0 }
  | .hooks = (.hooks // {})
  | .hooks.Stop = (
      ((.hooks.Stop // [])
        | map(.hooks |= map(select(.command != $hook))   # drop our previous entry
              | select((.hooks | length) > 0)))           # and any group it emptied
      + [ { hooks: [ { type: "command", command: $hook } ] } ]
    )
' "$SETTINGS" > "$TMP" && mv "$TMP" "$SETTINGS"

say "Merged statusLine + Stop hook into settings.json"
printf '\n\033[32mInstalled.\033[0m Restart Claude Code (or run /hooks) so the Stop hook loads.\n'
printf 'Status bar is live immediately. Tunables live in the script headers; uninstall with ./uninstall.sh\n'
