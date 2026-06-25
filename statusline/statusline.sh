#!/usr/bin/env bash
# Claude Code status line (macOS bash 3.2 compatible).
# Keeps context-window fill always visible so you can /compact manually before
# auto-compaction. Shows: dir+branch (+PR), model+effort, a context gauge,
# session churn + token usage, 5h/7d rate-limit usage, and session cost.
# Configured via ~/.claude/settings.json -> statusLine.

input="$(cat)"

# One jq call, one value per line; a while-read loop (not mapfile) keeps this
# working on bash 3.2 and preserves empty fields.
F=()
while IFS= read -r _l; do F+=("$_l"); done < <(printf '%s' "$input" | jq -r '
  (.model.display_name // "claude"),
  (.context_window.used_percentage // 0 | floor),
  (.context_window.total_input_tokens // 0),
  (.context_window.context_window_size // 0),
  (.exceeds_200k_tokens // false),
  (.context_window.total_output_tokens // 0),
  (.context_window.current_usage.cache_read_input_tokens // 0),
  (.cost.total_cost_usd // 0),
  (.rate_limits.five_hour.used_percentage // 0 | floor),
  (.rate_limits.seven_day.used_percentage // 0 | floor),
  (.effort.level // ""),
  (.workspace.current_dir // .cwd // "."),
  (.worktree.branch // ""),
  (.pr.number // ""),
  (.pr.review_state // ""),
  (.cost.total_lines_added // 0),
  (.cost.total_lines_removed // 0)
')
MODEL="${F[0]}"; CTX_PCT="${F[1]:-0}"; CTX_USED="${F[2]:-0}"; CTX_SIZE="${F[3]:-0}"
EXCEEDED="${F[4]}"; OUT_TOK="${F[5]:-0}"; CACHE_READ="${F[6]:-0}"; COST="${F[7]:-0}"
FIVE_H="${F[8]:-0}"; SEVEN_D="${F[9]:-0}"; EFFORT="${F[10]}"; DIRPATH="${F[11]:-.}"
WT_BRANCH="${F[12]}"; PR_NUM="${F[13]}"; PR_STATE="${F[14]}"; ADD="${F[15]:-0}"; DEL="${F[16]:-0}"

# --- colors ---
RST=$'\033[0m'; DIM=$'\033[2m'; BOLD=$'\033[1m'
GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; CYAN=$'\033[36m'; BLUE=$'\033[34m'; MAGENTA=$'\033[35m'

fmt_tokens() { # 1500->1k, 156000->156k, 1000000->1.0M
  local n="${1:-0}"
  if   [ "$n" -ge 1000000 ]; then printf '%d.%dM' $((n/1000000)) $(((n%1000000)/100000))
  elif [ "$n" -ge 1000 ];   then printf '%dk' $((n/1000))
  else printf '%d' "$n"; fi
}

gauge() { # render a 10-cell ▓/░ bar for a 0-100 percentage
  local pct="${1:-0}" w=10 filled i out=""
  filled=$(( pct * w / 100 )); [ "$filled" -gt "$w" ] && filled=$w; [ "$filled" -lt 0 ] && filled=0
  for ((i=0; i<w; i++)); do if [ "$i" -lt "$filled" ]; then out="${out}▓"; else out="${out}░"; fi; done
  printf '%s' "$out"
}

# --- context segment (the /compact cue): color + gauge + tokens + warning ---
ctx_color="$GREEN"; warn=""
if [ "$EXCEEDED" = "true" ] || [ "$CTX_PCT" -ge 90 ]; then
  ctx_color="$RED"; warn=" ${RED}${BOLD}⚠ /compact${RST}"
elif [ "$CTX_PCT" -ge 70 ]; then
  ctx_color="$YELLOW"
fi
CTX_SEG="${ctx_color}ctx ${BOLD}$(gauge "$CTX_PCT") ${CTX_PCT}%${RST}${ctx_color} $(fmt_tokens "$CTX_USED")/$(fmt_tokens "$CTX_SIZE")${RST}${warn}"

# --- git location (worktree field first; git fallback is fast) ---
BRANCH="$WT_BRANCH"
[ -z "$BRANCH" ] && BRANCH="$(git -C "$DIRPATH" branch --show-current 2>/dev/null)"
LOC="${CYAN}$(basename "$DIRPATH")${RST}"
[ -n "$BRANCH" ] && LOC="${LOC} ${DIM}⎇${RST}${MAGENTA}${BRANCH}${RST}"
if [ -n "$PR_NUM" ]; then
  pr_color="$DIM"
  case "$PR_STATE" in
    APPROVED) pr_color="$GREEN" ;;
    CHANGES_REQUESTED) pr_color="$RED" ;;
    REVIEW_REQUIRED|COMMENTED) pr_color="$YELLOW" ;;
  esac
  LOC="${LOC} ${pr_color}⇡#${PR_NUM}${RST}"
fi

# --- rate limits: color by the busier window ---
rl_color="$GREEN"; hi=$(( FIVE_H > SEVEN_D ? FIVE_H : SEVEN_D ))
if [ "$hi" -ge 90 ]; then rl_color="$RED"; elif [ "$hi" -ge 75 ]; then rl_color="$YELLOW"; fi
RL_SEG="${rl_color}5h ${FIVE_H}% · 7d ${SEVEN_D}%${RST}"

# --- model / effort, churn + tokens, cost ---
MODEL_SEG="${BLUE}${MODEL}${RST}"
[ -n "$EFFORT" ] && MODEL_SEG="${MODEL_SEG} ${DIM}${EFFORT}${RST}"
WORK_SEG="${DIM}${GREEN}+${ADD}${RST}${DIM}/${RED}-${DEL}${RST}${DIM} · out $(fmt_tokens "$OUT_TOK") · cache $(fmt_tokens "$CACHE_READ")${RST}"
COST_SEG="$(printf '%s$%.2f%s' "$DIM" "$COST" "$RST")"

SEP="${DIM} │ ${RST}"
# Two rows so nothing is truncated at the terminal edge, no matter how wide the
# numbers get. Row 1: where + what model + how full the context is (the /compact
# cue). Row 2: session work + limits + cost.
printf '%s%s%s%s%s\n' "$LOC" "$SEP" "$MODEL_SEG" "$SEP" "$CTX_SEG"
printf '%s%s%s%s%s\n' "$WORK_SEG" "$SEP" "$RL_SEG" "$SEP" "$COST_SEG"

# Row 3 (only when present): model/mode hint written by the UserPromptSubmit
# hook for the most recent prompt. The status line can't see the prompt, so the
# hook bridges it through this file.
HINT="$(cat "$HOME/.claude/.model-hint" 2>/dev/null)"
if [ -n "$HINT" ]; then
  printf '%s%s%s\n' "$YELLOW" "$HINT" "$RST"
fi

exit 0
