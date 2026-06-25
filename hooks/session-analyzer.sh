#!/usr/bin/env bash
# Stop hook — the single source of session hints.
#
# After each turn it gathers cheap signals about the session (turn count, approx
# context size, tool-usage histogram, repeated reads, search load, current
# model, available skills, whether a graphify graph exists) and asks a cheap
# model to synthesize the 1-3 highest-leverage optimizations RIGHT NOW —
# holistic advice (switch model, compact, use a skill/MCP/graph, offload reads
# to subagents, change approach), not raw counters.
#
# It auto-scales: the longer the session, the more often it runs, so long
# sessions get analysis presented automatically. Top line -> ~/.claude/.model-hint
# (status bar); full list -> ~/.claude/.session-report. ADVISORY ONLY.

[ -n "$MODEL_HINT_GUARD" ] && exit 0   # don't run inside the background claude -p

input="$(cat)"
transcript="$(printf '%s' "$input" | jq -r '.transcript_path // ""')"
cwd="$(printf '%s' "$input" | jq -r '.cwd // "."')"
sid="$(printf '%s' "$input" | jq -r '.session_id // "x"')"
[ -f "$transcript" ] || exit 0
command -v claude >/dev/null 2>&1 || exit 0

HINT_FILE="$HOME/.claude/.model-hint"
REPORT="$HOME/.claude/.session-report"

# Turn counter (per session) drives the auto-scaling cadence.
CF="$HOME/.claude/.sa-count-${sid}"
N=$(( $(cat "$CF" 2>/dev/null || echo 0) + 1 )); printf '%s' "$N" > "$CF" 2>/dev/null

# Cadence: short sessions rarely, long sessions almost every turn.
if   [ "$N" -lt 10 ]; then K=10
elif [ "$N" -lt 25 ]; then K=5
else K=2
fi
[ $(( N % K )) -ne 0 ] && exit 0

SLICE="$(tail -n 3000 "$transcript")"

# --- gather signals (token-free) ----------------------------------------------
HIST="$(printf '%s\n' "$SLICE" | jq -rs '
  [ .[] | (.message.content? // []) | if type=="array" then .[] else empty end
    | select(.type=="tool_use") | .name ]
  | group_by(.) | map("\(.[0]):\(length)") | join(" ")')"

read -r MODEL CTXTOK < <(printf '%s\n' "$SLICE" | jq -rs '
  [ .[] | select(.message.role=="assistant") ] as $a
  | (($a | last | .message.model) // "?") as $m
  | (($a | map(.message.usage // empty) | last) // {}) as $u
  | "\($m) \(($u.input_tokens // 0) + ($u.cache_read_input_tokens // 0))"')

read -r GREPS DUPS < <(printf '%s\n' "$SLICE" | jq -rs '
  [ .[] | (.message.content? // []) | if type=="array" then .[] else empty end
    | select(.type=="tool_use") ] as $t
  | (([ $t[] | select(.name=="Grep") ] | length)
     + ([ $t[] | select(.name=="Bash")
          | select((.input.command? // "") | test("\\b(grep|rg|find)\\b")) ] | length)) as $g
  | ([ $t[] | select(.name=="Read") | .input.file_path ]
       | group_by(.) | map(select(length>=3)) | length) as $d
  | "\($g) \($d)"')

RECENT="$(printf '%s\n' "$SLICE" | jq -r 'select(.message.role=="user") | .message.content
          | if type=="array" then (map(select(.type=="text").text)|join(" ")) else . end' \
          2>/dev/null | tail -n 8 | tr '\n' ' ')"

GRAPH="no"; [ -f "$cwd/graphify-out/graph.json" ] && GRAPH="yes"
SKILLS="$( { ls -1 "$cwd/.codex/skills" 2>/dev/null; ls -1 "$cwd/.claude/skills" 2>/dev/null; \
            ls -1 "$HOME/.claude/skills" 2>/dev/null; } | sort -u | tr '\n' ' ')"

# When there is NO graph, estimate how long building one would take from the
# repo's source-file count, so the suggestion can ask permission with a real ETA.
FILES="?"; GRAPHEST="n/a"
if [ "$GRAPH" = "no" ]; then
  FILES="$( (cd "$cwd" 2>/dev/null && git ls-files 2>/dev/null) \
            | grep -cE '\.(go|ts|tsx|js|jsx|py|rs|java|rb|c|cc|cpp|h|hpp|cs|kt|swift)$' )"
  [ -z "$FILES" ] && FILES=0
  if [ "$FILES" -eq 0 ]; then
    FILES="$(find "$cwd" -type f \( -name '*.go' -o -name '*.ts' -o -name '*.tsx' \
              -o -name '*.py' -o -name '*.rs' -o -name '*.js' \) 2>/dev/null | head -30000 | wc -l | tr -d ' ')"
  fi
  if   [ "$FILES" -lt 300 ];  then GRAPHEST="at least ~1-2 min"
  elif [ "$FILES" -lt 1000 ]; then GRAPHEST="at least ~2-4 min"
  elif [ "$FILES" -lt 3000 ]; then GRAPHEST="at least ~4-8 min"
  elif [ "$FILES" -lt 6000 ]; then GRAPHEST="at least ~8-15 min"
  else GRAPHEST="15+ min"
  fi
fi

SIGNALS="turns=$N  model=$MODEL  approx_context_tokens=$CTXTOK
tool_histogram: $HIST
searches=$GREPS  files_reread_3x+=$DUPS
graphify_graph=$GRAPH  repo_source_files=$FILES  est_graph_build=$GRAPHEST
available_skills: $SKILLS
recent_prompts: $RECENT"

INSTR='You optimize an ongoing coding-agent session for fewer tokens and more effectiveness.
From the SIGNALS, give the 1-3 highest-leverage moves to make RIGHT NOW. Think holistically:
- switch to a cheaper model if the work no longer needs the strongest one;
- /compact if the context is getting large relative to the work in flight;
- use a named available skill, an MCP, or the graphify graph instead of manual searching/reading;
- if graphify_graph=yes, recommend `graphify query` instead of grep/find for code lookups;
- if graphify_graph=no AND there is non-trivial searching, recommend BUILDING the graph: ask the
  user permission to run `/graphify .`, and state it takes est_graph_build for this repo size
  (repo_source_files files), noting it then cuts future search tokens. Phrase it as a question.
- offload large file reads or wide searches to subagents to keep the main context lean;
- change approach to avoid repeated or redundant work.
Be practical and holistic — do NOT nitpick exact counts. Recommend by name when you can.
Each line under 110 chars, start with an emoji. If the session is already efficient, output exactly: ✅ session looks efficient.'

# Fire-and-forget; detach so the turn never waits.
nohup bash -c '
  out="$(MODEL_HINT_GUARD=1 claude -p --model haiku "$0

SIGNALS:
$1" 2>/dev/null \
    | sed "s/\*\*//g" \
    | LC_ALL=C grep "^[^ -~[:space:]]" \
    | head -n 3)"
  if [ -n "$out" ]; then
    printf "%s\n" "$out" > "$2"
    printf "%s" "$out" | head -n 1 > "$3"
  fi
' "$INSTR" "$SIGNALS" "$REPORT" "$HINT_FILE" >/dev/null 2>&1 </dev/null &
disown 2>/dev/null

exit 0
