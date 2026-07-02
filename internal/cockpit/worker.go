package cockpit

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const instr = `You are the cockpit advisor for an ongoing Claude Code session.
Your job is not to do the work. Your job is to read the session instruments in SIGNALS and suggest
the 1-3 highest-leverage controls the user should apply RIGHT NOW to keep the run effective, cheap,
and controlled.

Think like an aircraft cockpit:
- gauges: context, model, rate/cost pressure, tool/search patterns, repeated reads, available tools;
- warnings: context getting large, expensive model overuse, repeated manual search, missing verifier;
- controls: /context, /compact, /clear, /model, plan mode, skills, subagents, MCP, graphify, verifier;
- checklists: ask before risky installs or broad tool changes; prefer reversible, local actions first.

Control logic:
- Model control: switch down when the work no longer needs the strongest model. Use Haiku for
  read-only exploration/subagents, Sonnet for normal coding, Opus only for hard architecture/debugging.
  Honor cost_index in SIGNALS: eco biases cheaper models, perf allows stronger models.
- Context control: judge fill by context_used_pct (the real window is already accounted for). Only urge
  /compact when context_used_pct is high (>= ~75%); run /context when the source of bloat is unclear;
  use /clear when switching to unrelated work.
- Budget control: if rate_5h_pct or rate_7d_pct is high (>= ~85%) or cost_usd is climbing fast, suggest
  switching to a cheaper model and/or delegating to cheap subagents to protect the remaining budget.
- Workflow control: use named available skills instead of restating workflows. Prefer /debug for
  failures, /code-review for review, /batch for repeated edits, /run and /verify for app checks,
  /loop for polling, /claude-api for API work, and project/user skills when names match prompts.
- Delegation control: use Explore/Plan/custom agents for broad reads, logs, or research so the main
  context only gets a summary. Recommend a cheaper agent model when suitable.
- Integration control: use MCP servers for external systems instead of pasted context: GitHub/Jira,
  Sentry/logs, Figma/designs, Slack/Notion/Drive docs, Postgres/DB queries, browser automation,
  official docs, or other connected sources.
- MCP control: if MCP servers are available, recommend the exact server name when obvious. Mention
  /mcp to inspect servers, @server:resource references, MCP prompt commands, and MCP Tool Search when
  many tools exist or schemas are bloating context.
- Tool gap signal: if the session's actual work — recent prompts, the tool histogram, repeated faults —
  shows an EXTERNAL capability being done the hard way (browser/UI/screenshot/E2E, a database, external
  docs/API references, design files, deep web research, logs/observability, repeated manual test loops,
  etc.) and available_mcp_servers has no matching server, add ONE extra final line, exactly:
  TOOLGAP: <capability phrase> || <evidence: one clause citing the concrete signal that shows the gap> || <web search query targeted at the capability AND repo_lang>
  Example:
  TOOLGAP: live browser control and screenshots || prompts ask for UI checks but Bash curl is used 12x || Playwright MCP server Claude Code browser automation
  A separate step will run the search — do NOT name a URL or invent a tool in your suggestion lines.
  A built-in skill does not replace a real integration. Omit the TOOLGAP line entirely if no external
  capability is in play; at most one TOOLGAP per run, for the highest-leverage gap.
- Code graph control: if graphify_graph=yes, recommend ` + "`graphify query`" + ` instead of grep/find.
  If graphify_graph=no and searching is non-trivial, ask permission to run ` + "`/graphify .`" + ` and
  state est_graph_build for repo_source_files files.
- Fault control: tool_errors/not_found_errors count failed tool calls; error_top names the worst tools.
  A high not_found count means paths/symbols are being guessed — suggest Glob or graphify query before
  Read/Edit. If one tool dominates error_top, suggest a concrete change of approach for that tool
  (e.g. repeated Bash failures in a test loop -> /loop or /debug) rather than re-running it.
- Redundancy control: call out repeated reads/searches and suggest changing approach.
- Phase-aware advising: session_phase in SIGNALS is like ECAM flight phase — PREFLIGHT favors discovery,
  CRUISE favors steady coding, APPROACH favors review/CI, LANDING favors wrap-up, EMER is context/rate
  critical. Warn from current instruments only; the pilot pulls the lever.

Be practical and holistic. Do not nitpick exact counts. Prefer a concrete control action over generic
advice. Recommend by name when you can.
Prefix each suggestion line with a severity tag and pipe: WARN|, CAUT|, ADV|, or MEMO| then an emoji
and one full sentence. Examples:
WARN|⚠️ Context at 92%% — run /context then /compact now.
CAUT|🔍 40 searches logged — switch to graphify query.
ADV|💰 Delegate log scanning to Explore (Haiku).
MEMO|✅ session looks efficient.
If the session is already efficient, output exactly: MEMO|✅ session looks efficient.`

const searchInstr = `You are the cockpit tool scout. An advisor analyzed a live Claude Code session and found a
capability being done the hard way. Find the single best CURRENT, well-maintained integration
(an MCP server, Claude Code plugin, or skill) that closes it.

CAPABILITY GAP: %s
SESSION EVIDENCE: %s
SUGGESTED SEARCH QUERY: %s
PROJECT STACK: %s
ALREADY AVAILABLE (never suggest these, or anything they already cover): %s

Method:
1. Run 1-3 web searches, starting from the suggested query; refine with the project stack if results
   are generic.
2. Prefer official/first-party integrations, then the curated shortlist below when the category
   matches, then the best community option with recent maintenance and real adoption.
3. Reject anything already available above, apparently unmaintained, or a poor fit for the stack or
   the evidence (the tool must fix what the session actually struggled with).

Curated shortlist by category:
- browser automation / E2E / screenshots: Playwright MCP (microsoft/playwright-mcp)
- session analytics / error patterns: sniffly (chiphuyen/sniffly)
- productivity reports / prompt coaching: vibe-log-cli (vibe-log/vibe-log-cli)
- token/cost baselining: ccusage
- team observability / dashboards: Claude Code OpenTelemetry export + SigNoz
- library/API docs lookup: Context7 MCP; GitHub PRs/issues: official GitHub MCP
- design files: Figma MCP; databases: official Postgres/SQLite MCP servers
- deep web research: a web-search MCP (e.g. Tavily/Exa)

Reply with EXACTLY one line and nothing else: an emoji, the tool name, a short why tied to the
evidence, and its source URL, phrased as an audit-first suggestion. Example:
🔌 Audit Playwright MCP for the UI checks now done via curl — https://github.com/microsoft/playwright-mcp
If you cannot find a credible match, reply with an empty line.`

// RunWorker reads signals for a session and writes suggestions to that
// session's report. Two phases: (1) a fast local advisor; (2) only if that
// flags a TOOLGAP, a focused web search for a concrete tool. Falls back to
// reversionary rule-based hints if haiku is unavailable or returns nothing.
func RunWorker(sigPath, session, cwd string) {
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		logf(session, "worker: read signals %s: %v", sigPath, err)
		return
	}
	logf(session, "worker: start (signals %d bytes)", len(sig))

	reversionary := false
	prompt := instr + "\n\nSIGNALS:\n" + string(sig)
	if seen := readSeen(session).Texts; len(seen) > 0 {
		tail := seen
		if len(tail) > 12 {
			tail = tail[len(tail)-12:]
		}
		prompt += "\n\nALREADY SUGGESTED THIS SESSION (do not repeat these levers — propose different ones, or the memo if nothing new applies):\n- " +
			strings.Join(tail, "\n- ")
	}
	out1, err := runClaude("", prompt)
	if err != nil {
		logf(session, "worker: phase1 claude failed: %v — reversionary mode", err)
		reversionary = true
	} else {
		logf(session, "worker: phase1 output:\n%s", strings.TrimSpace(out1))
	}

	var classified []classifiedSuggestion
	if reversionary {
		classified = ruleBasedSuggestions(string(sig))
	} else {
		lines := advisorLines(out1, 3)
		gap, hasGap := extractToolGap(out1)

		if hasGap {
			logf(session, "worker: tool gap detected: %q (evidence: %q) -> web search", gap.Need, gap.Evidence)
			out2, err := runClaude("WebSearch", buildScoutPrompt(gap, string(sig)))
			if err != nil {
				logf(session, "worker: phase2 search failed: %v", err)
			} else {
				logf(session, "worker: phase2 search output:\n%s", strings.TrimSpace(out2))
				if tool := emojiLines(out2, 1); len(tool) > 0 {
					// a tool audit is an advisory, not an instrument warning.
					lines = append(lines, "ADV|"+tool[0])
					if len(lines) > 4 {
						lines = lines[:4]
					}
				}
			}
		}

		if len(lines) == 0 {
			logf(session, "worker: no suggestion lines — reversionary mode")
			classified = ruleBasedSuggestions(string(sig))
		} else {
			snap := readSnapshot(session)
			st, _ := readState()
			classified = make([]classifiedSuggestion, 0, len(lines))
			for _, ln := range lines {
				classified = append(classified, classifySuggestion(ln, snap, st))
			}
		}
	}

	if len(classified) == 0 {
		logf(session, "worker: no suggestion lines produced")
		return
	}
	stored := mergeSuggestions(session, cwd, classified)
	snap := readSnapshot(session)
	snap.AdvisorOK = !reversionary
	snap.PendingSuggestions = countApplyable(stored)
	if snap.Cwd == "" {
		snap.Cwd = cwd
	}
	writeSnapshot(session, snap)
	logf(session, "worker: stored %d suggestion line(s) after merge (%d incoming)", len(stored), len(classified))
}

func runClaude(allowTools, prompt string) (string, error) {
	args := []string{"-p", "--model", "haiku"}
	if allowTools != "" {
		args = append(args, "--allowedTools", allowTools)
	}
	cmd := exec.Command("claude", args...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(os.Environ(), "MODEL_HINT_GUARD=1")
	out, err := cmd.Output()
	return string(out), err
}

// toolGap is the advisor's structured gap analysis: what capability is missing,
// which session signal proves it, and how to search for a closer.
type toolGap struct {
	Need     string
	Evidence string
	Query    string
}

func extractToolGap(out string) (toolGap, bool) {
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		s, ok := strings.CutPrefix(ln, "TOOLGAP:")
		if !ok {
			continue
		}
		parts := strings.Split(s, "||")
		g := toolGap{Need: strings.TrimSpace(parts[0])}
		if len(parts) > 1 {
			g.Evidence = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(parts[1]), "evidence:"))
		}
		if len(parts) > 2 {
			g.Query = strings.TrimSpace(parts[2])
		}
		if g.Need == "" {
			continue
		}
		if g.Query == "" {
			g.Query = g.Need + " Claude Code MCP server"
		}
		return g, true
	}
	return toolGap{}, false
}

// buildScoutPrompt targets the phase-2 web search with the session's own
// analysis: the gap, its evidence, the project stack, and what is already
// installed (so the scout never re-suggests existing integrations).
func buildScoutPrompt(g toolGap, sig string) string {
	stack := parseSignalStr(sig, "repo_lang=")
	installed := strings.TrimSpace(strings.Join(strings.Fields(
		parseSignalStr(sig, "available_mcp_servers:")+" "+parseSignalStr(sig, "available_skills:")), " "))
	return fmt.Sprintf(searchInstr,
		g.Need,
		fallback(g.Evidence, "(none given)"),
		g.Query,
		fallback(stack, "unknown"),
		fallback(installed, "(none)"))
}

// parseSignalStr extracts the rest of the line following key in a signals blob.
func parseSignalStr(sig, key string) string {
	i := strings.Index(sig, key)
	if i < 0 {
		return ""
	}
	rest := sig[i+len(key):]
	if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[:j]
	}
	// keys embedded mid-line (repo_lang=Go  graphify_graph=...) end at a double space.
	if j := strings.Index(rest, "  "); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}
