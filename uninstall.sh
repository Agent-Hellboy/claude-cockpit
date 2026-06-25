#!/usr/bin/env bash
# claude-cockpit uninstaller. Removes the status line + Stop hook entries and the
# installed scripts. Backs up settings.json first. Other settings are untouched.
set -euo pipefail

CLAUDE_DIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
SETTINGS="$CLAUDE_DIR/settings.json"
STATUSLINE_DST="$CLAUDE_DIR/statusline.sh"
HOOK_DST="$CLAUDE_DIR/hooks/session-analyzer.sh"

say() { printf '\033[36m==>\033[0m %s\n' "$1"; }

command -v jq >/dev/null 2>&1 || { echo "jq required"; exit 1; }

if [ -f "$SETTINGS" ]; then
  cp "$SETTINGS" "$SETTINGS.bak.$(date +%Y%m%d%H%M%S)"
  TMP="$(mktemp)"
  jq --arg sl "$STATUSLINE_DST" --arg hook "$HOOK_DST" '
    (if .statusLine.command == $sl then del(.statusLine) else . end)
    | (if .hooks.Stop then
         .hooks.Stop |= (map(.hooks |= map(select(.command != $hook))
                             | select((.hooks | length) > 0)))
       else . end)
    | (if (.hooks.Stop // []) == [] then del(.hooks.Stop) else . end)
    | (if (.hooks // {}) == {} then del(.hooks) else . end)
  ' "$SETTINGS" > "$TMP" && mv "$TMP" "$SETTINGS"
  say "Removed cockpit entries from settings.json"
fi

# Remove transient state + scripts.
rm -f "$CLAUDE_DIR/.model-hint" "$CLAUDE_DIR/.session-report" "$CLAUDE_DIR"/.sa-count-* 2>/dev/null || true
rm -f "$STATUSLINE_DST" "$HOOK_DST"
say "Removed installed scripts and state."
printf '\033[32mUninstalled.\033[0m Restart Claude Code to drop the status line.\n'
