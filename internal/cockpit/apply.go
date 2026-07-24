package cockpit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// applyPlan is the structured fix the apply worker returns.
type applyPlan struct {
	Summary         string         `json:"summary"`
	ClaudeMDSection string         `json:"claude_md_section"`
	MCPServers      map[string]any `json:"mcp_servers"`
	ShellCommands   []string       `json:"shell_commands"`
	SkillName       string         `json:"skill_name"`
	SkillContent    string         `json:"skill_content"`
	Notes           string         `json:"notes"`
}

const applyInstr = `You are the cockpit fix applier. The user accepted a cockpit suggestion and wants it applied.
Read the SUGGESTION and optional SIGNALS, then reply with ONLY a single JSON object (no markdown fences) shaped like:
{
  "summary": "one-line description of what will change",
  "claude_md_section": "markdown block to append to project agent instruction files for workflow/rules (empty string if not needed)",
  "mcp_servers": { "server-name": { "command": "npx", "args": ["-y", "package"] } } or {},
  "shell_commands": ["safe non-destructive install commands"] or [],
  "skill_name": "kebab-case skill folder name or empty",
  "skill_content": "full SKILL.md markdown or empty",
  "notes": "manual follow-ups the user must do, or empty"
}
Rules:
- Prefer project instruction rules for workflow levers: graphify query, /loop polling, Explore subagent, model tiering, skills by name.
- Only add mcp_servers when the suggestion clearly needs a real integration; use well-known packages only.
- shell_commands: safe installs only (brew, npm -g, npx -y, curl to official repos). No secrets, no rm, no force flags.
- skill_content: only when a concrete project skill should exist; keep it short and actionable.
- Never invent API keys. If auth is required, put it in notes.
- Be minimal: one focused change that matches the suggestion.`

// RunList prints numbered applyable fixes and unnumbered notes for the session
// resolved from the current directory, and names that session so the isolation
// boundary is visible.
func RunList(w io.Writer) {
	cwd, _ := os.Getwd()
	session := resolveSession(cwd)
	lines := readSuggestions(session)
	if len(lines) == 0 {
		fmt.Fprintln(w, "No cockpit suggestions for this session.")
		return
	}
	fmt.Fprintf(w, "Session %s\n\n", shortSession(session))
	snap := readSnapshot(session)
	st, _ := readState()
	classified := parseSuggestionStore(lines, reportAge(session), snap, st)
	notes, fixes := partitionSuggestions(classified)
	for i, c := range fixes {
		fmt.Fprintf(w, "[%d] %s %s\n", i+1, c.Level.Display(), c.Text)
	}
	for _, c := range notes {
		fmt.Fprintf(w, "    %s %s\n", c.Level.Display(), c.Text)
	}
	if len(fixes) > 0 {
		fmt.Fprintln(w, "\nWire up a fix: cockpit apply <n>")
	}
}

// shortSession renders a session id compactly for display.
func shortSession(session string) string {
	if len(session) > 8 {
		return session[:8]
	}
	if session == "" {
		return "(unknown)"
	}
	return session
}

// applyableReportLines returns suggestions cockpit apply can act on, in report order.
func applyableReportLines(session string) []string {
	lines := readSuggestions(session)
	snap := readSnapshot(session)
	st, _ := readState()
	var out []string
	for _, ln := range lines {
		if isApplyable(classifySuggestion(ln, snap, st)) {
			out = append(out, ln)
		}
	}
	return out
}

// applyableReportIndex maps apply number (1-based) to the line index in the full report.
func applyableReportIndex(session string, n int) (int, error) {
	if n < 1 {
		return 0, fmt.Errorf("suggestion number must be >= 1")
	}
	lines := readSuggestions(session)
	snap := readSnapshot(session)
	st, _ := readState()
	applyN := 0
	for i, ln := range lines {
		if isApplyable(classifySuggestion(ln, snap, st)) {
			applyN++
			if applyN == n {
				return i + 1, nil
			}
		}
	}
	if applyN == 0 {
		return 0, fmt.Errorf("no applyable fixes available; run: cockpit list")
	}
	return 0, fmt.Errorf("fix #%d not found: only %d available; run: cockpit list", n, applyN)
}

// RunApply applies applyable fix n (1-based) from the session resolved for cwd.
// When yes is false, prompts on stdin.
func RunApply(n int, cwd string, yes, dryRun bool) error {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	session := resolveSession(cwd)
	applyable := applyableReportLines(session)
	if n > len(applyable) {
		if len(applyable) == 0 {
			return fmt.Errorf("no applyable fixes for this session (notes and slash-command reminders are not wired via apply)")
		}
		return fmt.Errorf("fix #%d not found: only %d available; run: cockpit list", n, len(applyable))
	}
	reportIdx, err := applyableReportIndex(session, n)
	if err != nil {
		return err
	}
	suggestion := stripSeverityPrefix(applyable[n-1])

	signals := readSessionSignals(session)
	prompt := applyInstr + "\n\nSUGGESTION:\n" + suggestion
	if signals != "" {
		prompt += "\n\nSIGNALS:\n" + signals
	}

	out, err := runClaude("", prompt)
	if err != nil {
		return fmt.Errorf("plan suggestion: %w", err)
	}
	plan, err := parseApplyPlan(out)
	if err != nil {
		return fmt.Errorf("parse plan: %w", err)
	}

	fmt.Println("Suggestion:", suggestion)
	fmt.Println()
	fmt.Println("Plan:", plan.Summary)
	if plan.ClaudeMDSection != "" {
		for _, p := range instructionPaths(cwd) {
			fmt.Printf("  • Update %s\n", p)
		}
	}
	for name := range plan.MCPServers {
		fmt.Printf("  • Add MCP server: %s\n", name)
	}
	for _, cmd := range plan.ShellCommands {
		fmt.Printf("  • Run: %s\n", cmd)
	}
	if plan.SkillName != "" && plan.SkillContent != "" {
		fmt.Printf("  • Install shared skill: .agent-flightdeck/skills/%s/SKILL.md\n", plan.SkillName)
	}
	if plan.Notes != "" {
		fmt.Printf("  • Note: %s\n", plan.Notes)
	}
	fmt.Println()

	if dryRun {
		fmt.Println("Dry run — no changes made.")
		return nil
	}
	// The /cockpit slash command has no interactive stdin: the y/N prompt
	// would read EOF and silently cancel every apply. The command template
	// declares that explicitly via COCKPIT_ASSUME_YES=1 (env, not a TTY
	// heuristic — /dev/null is a char device and fools isatty-style checks).
	// The plan was just printed and its actions are safelisted.
	if !yes && os.Getenv("COCKPIT_ASSUME_YES") == "1" {
		fmt.Println("Non-interactive session — applying without prompt (use --dry-run to preview).")
		yes = true
	}
	if !yes {
		fmt.Print("Apply this fix? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		ans, _ := reader.ReadString('\n')
		ans = strings.TrimSpace(strings.ToLower(ans))
		if ans != "y" && ans != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	if err := executePlan(cwd, suggestion, plan); err != nil {
		return err
	}
	if err := removeSuggestion(session, reportIdx); err != nil {
		debugLog("apply: remove suggestion %d: %v", reportIdx, err)
	}
	fmt.Println("\033[32mApplied.\033[0m Restart Claude Code or run /hooks if MCP servers were added.")
	return nil
}

func parseApplyPlan(out string) (applyPlan, error) {
	out = strings.TrimSpace(out)
	// tolerate a prose preamble: grab the first { ... } block.
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end <= start {
		return applyPlan{}, fmt.Errorf("no JSON object in model output")
	}
	var plan applyPlan
	if err := json.Unmarshal([]byte(out[start:end+1]), &plan); err != nil {
		return applyPlan{}, err
	}
	if plan.Summary == "" {
		plan.Summary = "Apply cockpit suggestion"
	}
	return plan, nil
}

func executePlan(cwd, suggestion string, plan applyPlan) error {
	if plan.ClaudeMDSection != "" {
		if err := appendAgentInstructions(cwd, plan.ClaudeMDSection, suggestionMarker(suggestion)); err != nil {
			return fmt.Errorf("update agent instructions: %w", err)
		}
	}
	if len(plan.MCPServers) > 0 {
		if err := mergeMCPServers(cwd, plan.MCPServers); err != nil {
			return fmt.Errorf("merge MCP servers: %w", err)
		}
	}
	if plan.SkillName != "" && plan.SkillContent != "" {
		if err := writeSkill(cwd, plan.SkillName, plan.SkillContent); err != nil {
			return fmt.Errorf("write skill: %w", err)
		}
	}
	for _, cmd := range plan.ShellCommands {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		fmt.Printf("Running: %s\n", cmd)
		c := exec.Command("sh", "-c", cmd)
		c.Dir = cwd
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("command failed (%s): %w", cmd, err)
		}
	}
	return nil
}

func instructionPaths(cwd string) []string {
	return []string{sharedSkillPath(cwd, "agent-flightdeck")}
}

func suggestionMarker(suggestion string) string {
	s := strings.TrimSpace(suggestion)
	if len(s) > 48 {
		s = s[:48]
	}
	return "cockpit:" + strings.ReplaceAll(s, "\n", " ")
}

// appendAgentInstructions appends a marked section to the shared project skill
// so Claude Code, Codex, Cursor, and other coding agents read one canonical
// Agent Flightdeck guidance file.
func appendAgentInstructions(cwd, section, marker string) error {
	for _, path := range instructionPaths(cwd) {
		if err := appendInstructionFile(path, section, marker); err != nil {
			return err
		}
	}
	return nil
}

// appendClaudeMD is kept for older tests and callers.
func appendClaudeMD(cwd, section, marker string) error {
	return appendInstructionFile(filepath.Join(cwd, "CLAUDE.md"), section, marker)
}

func appendInstructionFile(path, section, marker string) error {
	markerLine := "<!-- " + marker + " -->"
	existing, _ := os.ReadFile(path)
	if strings.Contains(string(existing), markerLine) {
		fmt.Printf("%s already has this cockpit rule (%s)\n", path, marker)
		return nil
	}
	var b strings.Builder
	if len(existing) > 0 {
		b.Write(existing)
		if !strings.HasSuffix(string(existing), "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("\n")
	}
	b.WriteString(markerLine)
	b.WriteString("\n")
	b.WriteString(strings.TrimSpace(section))
	b.WriteString("\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func mergeMCPServers(cwd string, servers map[string]any) error {
	if len(servers) == 0 {
		return nil
	}
	path := filepath.Join(cwd, ".mcp.json")
	cfg := map[string]any{"mcpServers": map[string]any{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	mcp, _ := cfg["mcpServers"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
	}
	for name, spec := range servers {
		mcp[name] = spec
	}
	cfg["mcpServers"] = mcp
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func writeSkill(cwd, name, content string) error {
	name = strings.Trim(name, "/")
	if name == "" {
		return fmt.Errorf("empty skill name")
	}
	dir := filepath.Dir(sharedSkillPath(cwd, name))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(strings.TrimSpace(content)+"\n"), 0o644)
}

// readSessionSignals returns the resolved session's own signals — never another
// session's, which is what the old newest-file-wins scan did.
func readSessionSignals(session string) string {
	if session == "" {
		return ""
	}
	b, err := os.ReadFile(sessionSignalsFile(session))
	if err != nil {
		return ""
	}
	return string(b)
}

// removeSuggestion drops line n (1-based) from the session's report.
func removeSuggestion(session string, n int) error {
	lines := readSuggestions(session)
	if n < 1 || n > len(lines) {
		return nil
	}
	lines = append(lines[:n-1], lines[n:]...)
	if len(lines) == 0 {
		_ = os.Remove(sessionReportFile(session))
		return nil
	}
	st, _ := readSessionStamp(sessionReportFile(session))
	return writeReportLines(session, st.Cwd, lines)
}
