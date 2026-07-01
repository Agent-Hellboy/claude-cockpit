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
- Tool gap signal: if the recent prompts involve an EXTERNAL capability — browser/UI/screenshot/E2E, a
  database, external docs/API references, design files, deep web research, logs/observability, etc. —
  and available_mcp_servers has no matching server, add ONE extra final line, exactly:
  TOOLGAP: <short capability phrase>   (e.g. "TOOLGAP: browser automation and screenshots"). A separate
  step will search the web for a concrete tool — do NOT name or invent a tool/URL yourself. A built-in
  skill does not replace a real integration. Omit the TOOLGAP line entirely if no external capability
  is in play.
- Code graph control: if graphify_graph=yes, recommend ` + "`graphify query`" + ` instead of grep/find.
  If graphify_graph=no and searching is non-trivial, ask permission to run ` + "`/graphify .`" + ` and
  state est_graph_build for repo_source_files files.
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

const searchInstr = `Use web search to find the single best CURRENT, well-maintained, popular open-source
Claude Code integration for the need below: an MCP server, a Claude Code plugin, or a skill.
Need: %s

Reply with EXACTLY one line and nothing else: an emoji, the tool name, a short why, and its source URL,
phrased as an audit-first suggestion. Example:
🔌 Audit Playwright MCP for live browser control + screenshots — https://github.com/microsoft/playwright-mcp
If you cannot find a credible match, reply with an empty line.`

// RunWorker reads signals for a session and writes suggestions to the report +
// hint files. Two phases: (1) a fast local advisor; (2) only if that flags a
// TOOLGAP, a focused web search for a concrete tool. Falls back to reversionary
// rule-based hints if haiku is unavailable or returns nothing.
func RunWorker(sigPath, session string) {
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		logf(session, "worker: read signals %s: %v", sigPath, err)
		return
	}
	logf(session, "worker: start (signals %d bytes)", len(sig))

	reversionary := false
	out1, err := runClaude("", instr+"\n\nSIGNALS:\n"+string(sig))
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
		gap := extractToolGap(out1)

		if gap != "" {
			logf(session, "worker: tool gap detected: %q -> web search", gap)
			out2, err := runClaude("WebSearch", fmt.Sprintf(searchInstr, gap))
			if err != nil {
				logf(session, "worker: phase2 search failed: %v", err)
			} else {
				logf(session, "worker: phase2 search output:\n%s", strings.TrimSpace(out2))
				if tool := emojiLines(out2, 1); len(tool) > 0 {
					lines = append(lines, tool[0])
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
			snap := readSnapshot()
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
	if err := writeSuggestionReport(classified); err != nil {
		logf(session, "worker: write report: %v", err)
	}
	snap := readSnapshot()
	snap.AdvisorOK = !reversionary
	snap.PendingSuggestions = countApplyable(classified)
	writeSnapshot(snap)
	logf(session, "worker: wrote %d suggestion line(s); top=%q", len(classified), classified[0].Text)
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

func extractToolGap(out string) string {
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if s, ok := strings.CutPrefix(ln, "TOOLGAP:"); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
