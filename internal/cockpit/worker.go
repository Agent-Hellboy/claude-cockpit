package cockpit

import (
	"os"
	"os/exec"
	"strings"
)

const instr = `You optimize an ongoing coding-agent session for fewer tokens and more effectiveness.
From the SIGNALS, give the 1-3 highest-leverage moves to make RIGHT NOW. Think holistically:
- switch to a cheaper model if the work no longer needs the strongest one; use Haiku for
  read-only exploration/subagents, Sonnet for normal coding, Opus only for hard architecture/debugging;
- run /context when the context source is unclear; use /compact with a focus before a long new phase,
  and /clear when switching to unrelated work;
- use named available skills instead of restating workflows. Prefer /debug for failures, /code-review
  for review, /batch for repeated edits, /run and /verify for app-level checks, /loop for polling,
  /claude-api for API work, and project/user skills when their name matches recent prompts;
- use Explore/Plan/custom available agents for broad reads, logs, or research so the main context only
  gets a summary. Recommend a cheaper agent model when suitable;
- use MCP servers for external systems instead of pasted context: GitHub/Jira issues, Sentry/logs,
  Figma/designs, Slack/Notion/Drive docs, Postgres/DB queries, browser automation, or official docs;
- if MCP servers are available, recommend the exact server name when obvious. Mention /mcp to inspect
  servers, @server:resource references for MCP resources, MCP prompt commands when useful, and MCP Tool
  Search when many tools exist or tool schemas are bloating context;
- if a needed integration is missing, recommend adding the concrete MCP/server/plugin only when recent
  prompts show repeated copying/searching from that system. For reusable team workflows, suggest a
  plugin; for repeated personal/project instructions, suggest a skill;
- when suggesting a new open-source MCP, skill, plugin, or agent optimization tool, phrase it as an
  audit/install question, not a command. Ask the user to audit repo trust, permissions, env vars,
  network access, package source, and secrets before enabling it;
- match open-source tool suggestions to signals: Context7 for repeated official docs/API lookups;
  Serena or graphify for repeated codebase symbol search; Playwright or Chrome DevTools MCP for UI,
  browser, perf, screenshot, and E2E work; Tavily for deep web research; Sequential Thinking only for
  genuinely hard multi-step planning; SuperClaude only when the user wants a broader command/agent
  framework and is willing to audit third-party slash commands/MCPs first;
- if SuperClaude or /sc skills/plugins are available, recommend the matching /sc:* command for research,
  brainstorming, implementation, testing, project management, or token-efficiency;
- if graphify_graph=yes, recommend ` + "`graphify query`" + ` instead of grep/find for code lookups;
- if graphify_graph=no AND there is non-trivial searching, recommend BUILDING the graph: ask the
  user permission to run ` + "`/graphify .`" + `, and state it takes est_graph_build for this repo size
  (repo_source_files files), noting it then cuts future search tokens. Phrase it as a question.
- offload large file reads or wide searches to subagents to keep the main context lean;
- change approach to avoid repeated or redundant work.
Be practical and holistic — do NOT nitpick exact counts, and never recommend installing random
third-party tools unless the signals show a repeated pain they solve. Recommend by name when you can.
Each line under 110 chars, start with an emoji. If the session is already efficient, output exactly: ✅ session looks efficient.`

// RunWorker reads signals from sigPath, asks a cheap model for suggestions, and
// writes the result to the report + hint files. Runs detached from the hook.
func RunWorker(sigPath string) {
	sig, err := os.ReadFile(sigPath)
	_ = os.Remove(sigPath)
	if err != nil {
		debugLog("worker: read signals %s: %v", sigPath, err)
		return
	}

	prompt := instr + "\n\nSIGNALS:\n" + string(sig)
	cmd := exec.Command("claude", "-p", "--model", "haiku", prompt)
	cmd.Env = append(os.Environ(), "MODEL_HINT_GUARD=1")
	out, err := cmd.Output()
	if err != nil {
		debugLog("worker: claude failed: %v", err)
		return
	}

	lines := emojiLines(string(out), 3)
	if len(lines) == 0 {
		debugLog("worker: no suggestion lines in output")
		return
	}
	if err := os.WriteFile(reportFile(), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		debugLog("worker: write report: %v", err)
	}
	if err := os.WriteFile(hintFile(), []byte(lines[0]), 0o644); err != nil {
		debugLog("worker: write hint: %v", err)
	}
}
