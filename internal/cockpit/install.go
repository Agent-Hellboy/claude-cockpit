package cockpit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Install registers cockpit for one or more coding agents. With no target it
// preserves the historical behavior: install Claude Code hooks only.
func Install(targets ...string) error {
	if len(targets) == 0 {
		targets = []string{"claude"}
	}
	cwd, _ := os.Getwd()
	for _, target := range expandInstallTargets(targets) {
		switch target {
		case "claude":
			if err := installClaude(); err != nil {
				return err
			}
		case "codex":
			if err := installCodex(cwd); err != nil {
				return err
			}
		case "cursor":
			if err := installCursor(cwd); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown install target %q (use claude, codex, cursor, or all)", target)
		}
	}
	return nil
}

// installClaude registers cockpit into settings.json (statusLine + Stop hook),
// merging rather than overwriting so other settings/hooks are preserved.
// Idempotent. The merge is done in Go so the installer needs no jq.
func installClaude() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot resolve own path: %w", err)
	}
	exe, _ = filepath.Abs(exe)
	settingsPath := filepath.Join(ConfigDir(), "settings.json")

	m, err := loadSettings(settingsPath)
	if err != nil {
		return err
	}
	if err := backup(settingsPath); err != nil {
		return err
	}

	m["statusLine"] = map[string]any{
		"type":    "command",
		"command": quote(exe) + " statusline",
		"padding": 0,
	}
	setEventHook(m, "Stop", quote(exe)+" analyze", "analyze")
	setEventHook(m, "SessionEnd", quote(exe)+" cleanup", "cleanup")

	if err := writeSettings(settingsPath, m); err != nil {
		return err
	}
	if err := writeSlashCommand(exe); err != nil {
		fmt.Println("Slash command: could not write /cockpit —", err)
	} else {
		fmt.Println("Registered /cockpit slash command (systems · status · list · apply · checklist · daemon).")
	}
	fmt.Printf("\033[32mInstalled.\033[0m Registered cockpit in %s\n", settingsPath)
	fmt.Println("Restart Claude Code (or run /hooks) so the Stop hook loads. The status bar is live immediately.")
	fmt.Println("Accept a suggestion: cockpit apply <n>  (updates agent instructions, MCP, skills after you confirm)")
	if err := StartDaemonDetached(); err != nil {
		fmt.Println("Advisor daemon: start manually with  cockpit daemon start")
	} else {
		fmt.Println("Advisor daemon started (cockpit daemon status)")
	}
	return nil
}

func installCodex(cwd string) error {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if err := upsertManagedSection(filepath.Join(cwd, "AGENTS.md"), "codex", codexAgentInstructions()); err != nil {
		return err
	}
	if err := writeSharedSkill(cwd, "agent-flightdeck", flightdeckSkill()); err != nil {
		return err
	}
	fmt.Printf("\033[32mInstalled.\033[0m Registered Agent Flightdeck for Codex in %s\n", cwd)
	fmt.Println("Codex can now read AGENTS.md and the shared .agent-flightdeck skill.")
	return nil
}

func installCursor(cwd string) error {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if err := writeSharedSkill(cwd, "agent-flightdeck", flightdeckSkill()); err != nil {
		return err
	}
	fmt.Printf("\033[32mInstalled.\033[0m Registered shared Agent Flightdeck skill for Cursor in %s\n", sharedSkillPath(cwd, "agent-flightdeck"))
	return nil
}

// Uninstall removes cockpit's entries for one or more coding agents. With no
// target it preserves the historical behavior: uninstall Claude Code hooks only.
func Uninstall(targets ...string) error {
	if len(targets) == 0 {
		targets = []string{"claude"}
	}
	cwd, _ := os.Getwd()
	for _, target := range expandInstallTargets(targets) {
		switch target {
		case "claude":
			if err := uninstallClaude(); err != nil {
				return err
			}
		case "codex":
			if err := uninstallCodex(cwd); err != nil {
				return err
			}
		case "cursor":
			if err := uninstallCursor(cwd); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown uninstall target %q (use claude, codex, cursor, or all)", target)
		}
	}
	return nil
}

// uninstallClaude removes cockpit's entries from settings.json and deletes
// transient state. It leaves the binary in place (the installer/user manages that).
func uninstallClaude() error {
	_ = StopDaemon()
	exe, _ := os.Executable()
	exe, _ = filepath.Abs(exe)
	slCmd := quote(exe) + " statusline"
	settingsPath := filepath.Join(ConfigDir(), "settings.json")

	if _, err := os.Stat(settingsPath); err == nil {
		m, err := loadSettings(settingsPath)
		if err != nil {
			return err
		}
		_ = backup(settingsPath)

		if sl, ok := m["statusLine"].(map[string]any); ok {
			if cmd, _ := sl["command"].(string); cmd == slCmd || isCockpitSubcommand(cmd, "statusline") {
				delete(m, "statusLine")
			}
		}
		removeEventHook(m, "Stop", quote(exe)+" analyze", "analyze")
		removeEventHook(m, "SessionEnd", quote(exe)+" cleanup", "cleanup")
		if err := writeSettings(settingsPath, m); err != nil {
			return err
		}
	}

	_ = os.Remove(slashCommandPath())

	// transient state, consolidated under cockpitDir()
	dir := cockpitDir()
	_ = os.Remove(filepath.Join(dir, ".model-hint"))
	_ = os.Remove(filepath.Join(dir, ".session-report"))
	_ = os.Remove(filepath.Join(dir, ".cockpit-learning.json"))
	_ = os.Remove(filepath.Join(dir, ".cockpit-pending.json"))
	_ = os.Remove(filepath.Join(dir, ".cockpit-state"))
	_ = os.Remove(filepath.Join(dir, ".cockpit-snapshot"))
	_ = os.Remove(filepath.Join(dir, ".cockpit-chime-state"))
	_ = os.Remove(filepath.Join(dir, ".cockpit-debug.log"))
	_ = os.Remove(filepath.Join(dir, ".cockpit-daemon.pid"))
	_ = os.Remove(filepath.Join(dir, ".cockpit-daemon.log"))
	_ = os.Remove(filepath.Join(dir, "history.jsonl"))
	_ = os.RemoveAll(filepath.Join(dir, "cockpit-jobs"))
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			name := e.Name()
			transient := strings.HasPrefix(name, ".sa-count-") ||
				strings.HasSuffix(name, ".report") || strings.HasSuffix(name, ".snapshot") ||
				strings.HasSuffix(name, ".chime") || strings.HasSuffix(name, ".signals") ||
				strings.HasSuffix(name, ".seen")
			if transient {
				_ = os.Remove(filepath.Join(dir, name))
			}
		}
	}
	fmt.Println("\033[32mUninstalled.\033[0m Removed cockpit entries and state. Restart Claude Code to drop the status line.")
	return nil
}

func uninstallCodex(cwd string) error {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if err := removeManagedSection(filepath.Join(cwd, "AGENTS.md"), "codex"); err != nil {
		return err
	}
	fmt.Printf("\033[32mUninstalled.\033[0m Removed Agent Flightdeck Codex pointer from %s\n", cwd)
	return nil
}

func uninstallCursor(cwd string) error {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	fmt.Printf("\033[32mUninstalled.\033[0m Cursor uses the shared skill at %s; no Cursor rule is installed.\n", sharedSkillPath(cwd, "agent-flightdeck"))
	return nil
}

func loadSettings(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON — fix or move it, then retry: %w", path, err)
	}
	return m, nil
}

func writeSettings(path string, m map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func backup(path string) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	dst := fmt.Sprintf("%s.bak.%s", path, time.Now().Format("20060102150405"))
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("Backed up settings.json -> %s\n", dst)
	return nil
}

// setEventHook appends our hook for the given event, removing any prior cockpit
// entry (matched by exact command or by subcommand) first so re-running install
// never duplicates it. Foreign hooks are preserved.
func setEventHook(m map[string]any, event, cmd, sub string) {
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	list := filterOutCockpitCommand(toList(hooks[event]), cmd, sub)
	list = append(list, map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": cmd}},
	})
	hooks[event] = list
	m["hooks"] = hooks
}

func removeEventHook(m map[string]any, event, cmd, sub string) {
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		return
	}
	list := filterOutCockpitCommand(toList(hooks[event]), cmd, sub)
	if len(list) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = list
	}
	if len(hooks) == 0 {
		delete(m, "hooks")
	} else {
		m["hooks"] = hooks
	}
}

// filterOutCommand drops hook groups that contain the given command, and any
// group left with no inner hooks.
func filterOutCommand(groups []any, cmd string) []any {
	return filterOutCockpitCommand(groups, cmd, "")
}

func filterOutCockpitCommand(groups []any, cmd, subcommand string) []any {
	out := make([]any, 0, len(groups))
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			out = append(out, g)
			continue
		}
		inner := toList(gm["hooks"])
		kept := make([]any, 0, len(inner))
		for _, h := range inner {
			if hm, ok := h.(map[string]any); ok {
				if c, _ := hm["command"].(string); c == cmd || isCockpitSubcommand(c, subcommand) {
					continue
				}
			}
			kept = append(kept, h)
		}
		if len(kept) == 0 {
			continue
		}
		gm["hooks"] = kept
		out = append(out, gm)
	}
	return out
}

func isCockpitSubcommand(cmd, subcommand string) bool {
	if subcommand == "" {
		return false
	}
	return strings.HasSuffix(cmd, " "+subcommand) && strings.Contains(cmd, "cockpit")
}

func toList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

// quote single-quotes a path for safe use in a shell command string.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func slashCommandPath() string {
	return filepath.Join(ConfigDir(), "commands", "cockpit.md")
}

// cockpitCommandMD is the /cockpit slash-command definition. {{EXE}} is replaced
// with the absolute, shell-quoted binary path at install time so it works under a
// custom CLAUDE_CONFIG_DIR. The embedded shell defaults a bare /cockpit to the
// `systems` synoptic, and passes any argument (status, list, apply <n>,
// checklist <topic>, daemon status, debrief) straight through to the binary.
//
// $ARGUMENTS is text-substituted by Claude Code, so multi-word input like
// `apply 1` must be re-split into separate argv entries. We route through an
// inner `sh -c` where the substituted tokens arrive as positional parameters
// ($@) rather than being re-expanded from a variable: an unquoted ${VAR}
// word-splits in bash but NOT in zsh (macOS default), which previously collapsed
// `apply 1` into a single argument and produced `unknown subcommand "apply 1"`.
// Passing the tokens as positionals splits identically in every POSIX shell.
//
// CRITICAL: the template must not contain ANY $<digit> ($0-$9) — Claude Code
// substitutes those placeholders with the slash command's own arguments even
// inside single quotes, ZERO-indexed: a previous `b=$1` became `b=1` (second
// arg) and `exec "$0"` became `exec "apply"` (first arg). The binary path is
// therefore carried in the $COCKPIT_BIN env var — named variables, $#, and $@
// all survive substitution (only $ARGUMENTS and $<digit> are rewritten).
// COCKPIT_ASSUME_YES=1 tells `apply` there is no interactive stdin here, so it
// must not wait on a y/N prompt that would read EOF and cancel.
const cockpitCommandMD = "---\n" +
	"description: Manage Agent Flightdeck — synoptic, status, suggestions, apply, daemon\n" +
	"argument-hint: \"[systems | status | list | apply <n> | checklist <topic> | plan | debrief | daemon status]\"\n" +
	"allowed-tools: Bash\n" +
	"---\n\n" +
	"Run the cockpit control below, then explain the output plainly to the user:\n" +
	"summarize what each section means, call out anything in the warning/caution\n" +
	"colors first, and if they asked to `apply <n>` state exactly what changed.\n\n" +
	"!`COCKPIT_BIN={{EXE}} sh -c 'if [ \"$#\" -eq 0 ]; then exec \"$COCKPIT_BIN\" systems; else COCKPIT_ASSUME_YES=1 exec \"$COCKPIT_BIN\" \"$@\"; fi' cockpit $ARGUMENTS 2>&1`\n"

func writeSlashCommand(exe string) error {
	path := slashCommandPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := strings.ReplaceAll(cockpitCommandMD, "{{EXE}}", quote(exe))
	return os.WriteFile(path, []byte(body), 0o644)
}

func expandInstallTargets(targets []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range targets {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if t == "all" {
			for _, expanded := range []string{"claude", "codex", "cursor"} {
				if !seen[expanded] {
					seen[expanded] = true
					out = append(out, expanded)
				}
			}
			continue
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func managedStart(id string) string { return "<!-- agent-flightdeck:" + id + ":start -->" }
func managedEnd(id string) string   { return "<!-- agent-flightdeck:" + id + ":end -->" }

func upsertManagedSection(path, id, section string) error {
	start, end := managedStart(id), managedEnd(id)
	existing, _ := os.ReadFile(path)
	body := strings.TrimSpace(section)
	prefix := ""
	if strings.HasSuffix(path, ".mdc") && strings.HasPrefix(body, "---\n") {
		rest := body[4:]
		if i := strings.Index(rest, "\n---"); i >= 0 {
			prefix = "---\n" + rest[:i] + "\n---\n\n"
			body = strings.TrimSpace(rest[i+4:])
		}
	}
	block := start + "\n" + body + "\n" + end
	text := string(existing)
	if text == "" {
		text = prefix + block + "\n"
	} else if s := strings.Index(text, start); s >= 0 {
		if e := strings.Index(text[s:], end); e >= 0 {
			e += s + len(end)
			text = text[:s] + block + text[e:]
		} else {
			text = strings.TrimRight(text, "\n") + "\n\n" + block + "\n"
		}
	} else {
		text = strings.TrimRight(text, "\n") + "\n\n" + block + "\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

func removeManagedSection(path, id string) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	text := string(b)
	start, end := managedStart(id), managedEnd(id)
	s := strings.Index(text, start)
	if s < 0 {
		return nil
	}
	e := strings.Index(text[s:], end)
	if e < 0 {
		return nil
	}
	e += s + len(end)
	text = strings.TrimSpace(text[:s] + text[e:])
	if text == "" || mdcFrontmatterOnly(text) {
		return os.Remove(path)
	}
	return os.WriteFile(path, []byte(text+"\n"), 0o644)
}

func mdcFrontmatterOnly(text string) bool {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "---\n") {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, "---"))
	if i := strings.Index(rest, "---"); i >= 0 {
		return strings.TrimSpace(rest[i+3:]) == ""
	}
	return false
}

func codexAgentInstructions() string {
	return `## Agent Flightdeck

- Use the shared project skill at ` + "`.agent-flightdeck/skills/agent-flightdeck/SKILL.md`" + ` for Agent Flightdeck cockpit controls.
- Use ` + "`cockpit memory --json`" + ` when another tool needs compact background memory of what happened in recent sessions.`
}

func flightdeckSkill() string {
	return `# Agent Flightdeck

Use this shared project skill from Claude Code, Codex, Cursor, or any other coding agent when a session should inspect, retrieve, or apply Agent Flightdeck controls.

## Workflow

1. Run ` + "`cockpit systems`" + ` to inspect configured coding agents, MCP servers, skills, shared skills, and graphify state.
2. Run ` + "`cockpit status`" + ` or ` + "`cockpit plan`" + ` when context, budget, or route drift needs attention.
3. Run ` + "`cockpit memory --json`" + ` when a downstream system needs compact session memory.
4. Run ` + "`cockpit checklist <topic>`" + ` for focused procedures such as context, budget, search, or faults.
5. Run ` + "`cockpit list`" + ` to inspect pending suggestions.
6. Use ` + "`cockpit apply <n> --dry-run`" + ` before accepting a suggested instruction, MCP, or skill change.

## Boundaries

- Treat cockpit suggestions as advisory.
- Prefer dry runs before project writes.
- Keep accepted controls in this shared skill so Claude Code, Codex, and Cursor read the same guidance.`
}

func writeSharedSkill(cwd, name, content string) error {
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
