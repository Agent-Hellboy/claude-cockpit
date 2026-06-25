package cockpit

import (
	"os"
	"os/exec"
	"strings"
)

const instr = `You optimize an ongoing coding-agent session for fewer tokens and more effectiveness.
From the SIGNALS, give the 1-3 highest-leverage moves to make RIGHT NOW. Think holistically:
- switch to a cheaper model if the work no longer needs the strongest one;
- /compact if the context is getting large relative to the work in flight;
- use a named available skill, an MCP, or the graphify graph instead of manual searching/reading;
- if graphify_graph=yes, recommend ` + "`graphify query`" + ` instead of grep/find for code lookups;
- if graphify_graph=no AND there is non-trivial searching, recommend BUILDING the graph: ask the
  user permission to run ` + "`/graphify .`" + `, and state it takes est_graph_build for this repo size
  (repo_source_files files), noting it then cuts future search tokens. Phrase it as a question.
- offload large file reads or wide searches to subagents to keep the main context lean;
- change approach to avoid repeated or redundant work.
Be practical and holistic — do NOT nitpick exact counts. Recommend by name when you can.
Each line under 110 chars, start with an emoji. If the session is already efficient, output exactly: ✅ session looks efficient.`

// RunWorker reads signals from sigPath, asks a cheap model for suggestions, and
// writes the result to the report + hint files. Runs detached from the hook.
func RunWorker(sigPath string) {
	sig, err := os.ReadFile(sigPath)
	_ = os.Remove(sigPath)
	if err != nil {
		return
	}

	prompt := instr + "\n\nSIGNALS:\n" + string(sig)
	cmd := exec.Command("claude", "-p", "--model", "haiku", prompt)
	cmd.Env = append(os.Environ(), "MODEL_HINT_GUARD=1")
	out, err := cmd.Output()
	if err != nil {
		return
	}

	lines := emojiLines(string(out), 3)
	if len(lines) == 0 {
		return
	}
	_ = os.WriteFile(reportFile(), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	_ = os.WriteFile(hintFile(), []byte(lines[0]), 0o644)
}
