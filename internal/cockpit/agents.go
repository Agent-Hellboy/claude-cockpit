package cockpit

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type codingAgent struct {
	ID          string
	Name        string
	ConfigDir   string
	ProjectDirs []string
	UserDirs    []string
	MCPFiles    []string
	RuleFiles   []string
}

func codingAgents(cwd string) []codingAgent {
	claudeDir := ConfigDir()
	codexDir := CodexConfigDir()
	cursorDir := CursorConfigDir()
	return []codingAgent{
		{
			ID:          "claude",
			Name:        "Claude Code",
			ConfigDir:   claudeDir,
			ProjectDirs: []string{filepath.Join(cwd, ".claude", "agents"), filepath.Join(cwd, ".claude", "skills")},
			UserDirs:    []string{filepath.Join(claudeDir, "agents"), filepath.Join(claudeDir, "skills"), filepath.Join(claudeDir, "plugins")},
			MCPFiles:    []string{filepath.Join(cwd, ".mcp.json"), filepath.Join(claudeDir, "settings.json"), homeClaudeJSON()},
			RuleFiles:   []string{filepath.Join(cwd, "CLAUDE.md")},
		},
		{
			ID:          "codex",
			Name:        "Codex",
			ConfigDir:   codexDir,
			ProjectDirs: []string{filepath.Join(cwd, ".codex", "agents"), filepath.Join(cwd, ".codex", "skills"), filepath.Join(cwd, ".agents")},
			UserDirs:    []string{filepath.Join(codexDir, "agents"), filepath.Join(codexDir, "skills"), filepath.Join(codexDir, "plugins")},
			MCPFiles:    []string{filepath.Join(cwd, ".mcp.json"), filepath.Join(codexDir, "mcp.json")},
			RuleFiles:   []string{filepath.Join(cwd, "AGENTS.md")},
		},
		{
			ID:          "cursor",
			Name:        "Cursor",
			ConfigDir:   cursorDir,
			ProjectDirs: []string{filepath.Join(cwd, ".cursor", "rules")},
			UserDirs:    []string{filepath.Join(cursorDir, "rules")},
			MCPFiles:    []string{filepath.Join(cwd, ".cursor", "mcp.json"), filepath.Join(cursorDir, "mcp.json")},
			RuleFiles:   []string{filepath.Join(cwd, ".cursor", "rules", "cockpit.mdc")},
		},
	}
}

// CodexConfigDir returns the Codex config directory, honoring CODEX_HOME.
func CodexConfigDir() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".codex"
	}
	return filepath.Join(home, ".codex")
}

// CursorConfigDir returns the Cursor config directory, honoring CURSOR_CONFIG_DIR.
func CursorConfigDir() string {
	if d := os.Getenv("CURSOR_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".cursor"
	}
	return filepath.Join(home, ".cursor")
}

func listCodingAgents(cwd string) string {
	parts := make([]string, 0, 3)
	for _, agent := range codingAgents(cwd) {
		project := existingDirs(agent.ProjectDirs)
		user := existingDirs(agent.UserDirs)
		state := "ready"
		if project == 0 && user == 0 {
			state = "not configured"
		}
		parts = append(parts, agent.ID+":"+state)
	}
	return strings.Join(parts, " ")
}

func allProjectDirs(cwd string, pick func(codingAgent) []string) []string {
	var dirs []string
	for _, agent := range codingAgents(cwd) {
		dirs = append(dirs, pick(agent)...)
	}
	return dirs
}

func existingDirs(dirs []string) int {
	n := 0
	for _, d := range dirs {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			n++
		}
	}
	return n
}

func sortedNames(set map[string]bool) string {
	names := make([]string, 0, len(set))
	for k := range set {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}
