package cockpit

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	codexCommandMarkerStart  = "# agent-flightdeck:codex-command:start"
	codexCommandMarkerEnd    = "# agent-flightdeck:codex-command:end"
	codexStatusMarkerStart   = "# agent-flightdeck:codex-statusline:start"
	codexStatusMarkerEnd     = "# agent-flightdeck:codex-statusline:end"
	cursorCommandMarkerStart = "<!-- agent-flightdeck:cursor-command:start -->"
	cursorCommandMarkerEnd   = "<!-- agent-flightdeck:cursor-command:end -->"
)

const codexStatusLineItems = `["model-with-reasoning", "current-dir", "git-branch", "context-remaining", "five-hour-limit", "weekly-limit", "fast-mode"]`

const codexCockpitPrompt = `---
description: Run Agent Flightdeck cockpit controls
argument-hint: "[systems | status | list | checklist <topic> | plan | debrief | daemon status]"
# agent-flightdeck:codex-command:start
# agent-flightdeck:codex-command:end
---

Use the Agent Flightdeck cockpit executable for this request.

- With no arguments, run cockpit systems.
- If arguments were provided after /prompts:cockpit, run cockpit $ARGUMENTS.
- Treat the output as advisory. Do not run cockpit apply unless the user explicitly requested it.
- Summarize warnings and deferred items first, then explain the useful controls plainly.
`

const cursorCockpitCommand = `<!-- agent-flightdeck:cursor-command:start -->
# Agent Flightdeck cockpit

Run the Agent Flightdeck cockpit executable for this request.

- With no arguments, run cockpit systems.
- If the user supplied arguments after /cockpit, pass them to cockpit (for example, /cockpit status runs cockpit status).
- Treat the output as advisory. Do not run cockpit apply unless the user explicitly requested it.
- Summarize warnings and deferred items first, then explain the useful controls plainly.
<!-- agent-flightdeck:cursor-command:end -->
`

func codexConfigPath() string { return filepath.Join(CodexConfigDir(), "config.toml") }

func codexPromptPath() string {
	return filepath.Join(CodexConfigDir(), "prompts", "cockpit.md")
}

func cursorCommandPath(cwd string) string {
	return filepath.Join(cwd, ".cursor", "commands", "cockpit.md")
}

func writeCodexPrompt() (bool, error) {
	return writeOwnedFile(codexPromptPath(), codexCommandMarkerStart, codexCockpitPrompt)
}

func writeCursorCommand(cwd string) (bool, error) {
	return writeOwnedFile(cursorCommandPath(cwd), cursorCommandMarkerStart, cursorCockpitCommand)
}

// writeOwnedFile creates or refreshes a file written by Agent Flightdeck. An
// existing file without our marker is preserved so a user's command or prompt
// with the same name is never overwritten.
func writeOwnedFile(path, marker, content string) (bool, error) {
	existing, err := os.ReadFile(path)
	if err == nil && !strings.Contains(string(existing), marker) {
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o644)
}

func removeOwnedFile(path, marker string) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.Contains(string(b), marker) {
		return nil
	}
	return os.Remove(path)
}

// upsertCodexStatusLine adds Codex's native status-line fields without
// replacing a user's existing [tui].status_line selection. The managed markers
// let later installs refresh this release's defaults and let uninstall remove
// only what Flightdeck added.
func upsertCodexStatusLine(path string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := string(existing)
	block := codexStatusLineBlock()

	if replaced, ok := replaceMarkedBlock(text, codexStatusMarkerStart, codexStatusMarkerEnd, "status_line = "+codexStatusLineItems); ok {
		return os.WriteFile(path, []byte(replaced), 0o644)
	}

	if start, end, ok := tomlTableBounds(text, "tui"); ok {
		if tomlTableHasKey(text, start, end, "status_line") {
			return nil
		}
		prefix := text[:start]
		if !strings.HasSuffix(prefix, "\n") {
			prefix += "\n"
		}
		text = prefix + block + "\n" + text[start:]
	} else {
		trimmed := strings.TrimRight(text, "\n")
		if trimmed == "" {
			text = "[tui]\n" + block + "\n"
		} else {
			text = trimmed + "\n\n[tui]\n" + block + "\n"
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

func removeCodexStatusLine(path string) error {
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	text, ok := replaceMarkedBlock(string(existing), codexStatusMarkerStart, codexStatusMarkerEnd, "")
	if !ok {
		return nil
	}
	text = strings.TrimSpace(text)
	if text == "" || text == "[tui]" {
		return os.Remove(path)
	}
	return os.WriteFile(path, []byte(text+"\n"), 0o644)
}

func codexStatusLineBlock() string {
	return codexStatusMarkerStart + "\nstatus_line = " + codexStatusLineItems + "\n" + codexStatusMarkerEnd
}

func codexStatusLineInstalled() bool {
	b, err := os.ReadFile(codexConfigPath())
	if err != nil {
		return false
	}
	text := string(b)
	if strings.Contains(text, codexStatusMarkerStart) {
		return true
	}
	if start, end, ok := tomlTableBounds(text, "tui"); ok {
		return tomlTableHasKey(text, start, end, "status_line")
	}
	return false
}

func replaceMarkedBlock(text, startMarker, endMarker, body string) (string, bool) {
	start := strings.Index(text, startMarker)
	if start < 0 {
		return text, false
	}
	relEnd := strings.Index(text[start+len(startMarker):], endMarker)
	if relEnd < 0 {
		return text, false
	}
	end := start + len(startMarker) + relEnd
	endAfter := end + len(endMarker)
	if body == "" {
		return text[:start] + text[endAfter:], true
	}
	block := startMarker
	block += "\n" + body
	block += "\n" + endMarker
	return text[:start] + block + text[endAfter:], true
}

func tomlTableBounds(text, target string) (start, end int, ok bool) {
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") && !strings.HasPrefix(trimmed, "[[") {
			name := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			if ok {
				return start, offset, true
			}
			if name == target {
				start = offset + len(line)
				ok = true
			}
		}
		offset += len(line)
	}
	if ok {
		return start, len(text), true
	}
	return 0, 0, false
}

func tomlTableHasKey(text string, start, end int, key string) bool {
	for _, line := range strings.Split(text[start:end], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, key+"=") || strings.HasPrefix(line, key+" =") {
			return true
		}
	}
	return false
}
