package cockpit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ANSI colors.
const (
	rst     = "\033[0m"
	dim     = "\033[2m"
	bold    = "\033[1m"
	green   = "\033[32m"
	yellow  = "\033[33m"
	red     = "\033[31m"
	cyan    = "\033[36m"
	blue    = "\033[34m"
	magenta = "\033[35m"
)

// ConfigDir returns the Claude Code config directory, honoring CLAUDE_CONFIG_DIR.
func ConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}

func hintFile() string   { return filepath.Join(ConfigDir(), ".model-hint") }
func reportFile() string { return filepath.Join(ConfigDir(), ".session-report") }

// fmtTokens renders a token count compactly: 1500->1k, 156000->156k, 1000000->1.0M.
func fmtTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%d.%dM", n/1_000_000, (n%1_000_000)/100_000)
	case n >= 1000:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// gauge renders a 10-cell ▓/░ bar for a 0-100 percentage.
func gauge(pct int) string {
	const w = 10
	filled := pct * w / 100
	if filled > w {
		filled = w
	}
	if filled < 0 {
		filled = 0
	}
	var b strings.Builder
	for i := 0; i < w; i++ {
		if i < filled {
			b.WriteString("▓")
		} else {
			b.WriteString("░")
		}
	}
	return b.String()
}

// emojiLines keeps only model output lines that begin with a non-ASCII rune
// (an emoji), strips markdown bold, and returns at most max lines. This makes
// the filter independent of which emoji the model happens to choose.
func emojiLines(out string, max int) []string {
	var res []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.ReplaceAll(line, "**", "")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		r, _ := utf8.DecodeRuneInString(line)
		if r == utf8.RuneError || r < 0x80 {
			continue // ASCII-led line => prose/preamble, drop it
		}
		res = append(res, line)
		if len(res) >= max {
			break
		}
	}
	return res
}
