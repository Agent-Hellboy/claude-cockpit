package cockpit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Install registers cockpit into settings.json (statusLine + Stop hook), merging
// rather than overwriting so other settings/hooks are preserved. Idempotent.
// The merge is done in Go so the installer needs no jq. Returns an error string
// on failure (callers print + exit).
func Install() error {
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
	fmt.Printf("\033[32mInstalled.\033[0m Registered cockpit in %s\n", settingsPath)
	fmt.Println("Restart Claude Code (or run /hooks) so the Stop hook loads. The status bar is live immediately.")
	return nil
}

// Uninstall removes cockpit's entries from settings.json and deletes transient
// state. It leaves the binary in place (the installer/user manages that).
func Uninstall() error {
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

	// transient state
	dir := ConfigDir()
	_ = os.Remove(filepath.Join(dir, ".model-hint"))
	_ = os.Remove(filepath.Join(dir, ".session-report"))
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if len(e.Name()) > 10 && e.Name()[:10] == ".sa-count-" {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	fmt.Println("\033[32mUninstalled.\033[0m Removed cockpit entries and state. Restart Claude Code to drop the status line.")
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
