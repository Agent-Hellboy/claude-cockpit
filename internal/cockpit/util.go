package cockpit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Catppuccin Mocha — a balanced 24-bit palette designed to be easy on the eyes
// on dark backgrounds: rich, harmonious hues rather than harsh saturation, and a
// soft (not washed-out) secondary for labels/separators.
const (
	rst     = "\033[0m"
	bold    = "\033[1m"
	green   = "\033[38;2;166;227;161m" // Green
	yellow  = "\033[38;2;249;226;175m" // Yellow
	red     = "\033[38;2;243;139;168m" // Red
	cyan    = "\033[38;2;148;226;213m" // Teal
	blue    = "\033[38;2;137;180;250m" // Blue
	magenta = "\033[38;2;203;166;247m" // Mauve
	dim     = "\033[38;2;147;153;178m" // Overlay2 — soft, readable secondary
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
func debugFile() string  { return filepath.Join(ConfigDir(), ".cockpit-debug.log") }

// stateFile holds the authoritative context/cost/rate snapshot the status line
// receives from Claude Code, so the Stop-hook analyzer (which is not given that
// data) can read the real context window instead of guessing from the model name.
func stateFile() string { return filepath.Join(ConfigDir(), ".cockpit-state") }

func debugLog(format string, args ...any) {
	if os.Getenv("COCKPIT_DEBUG") != "1" {
		return
	}
	msg := fmt.Sprintf("%s "+format+"\n", append([]any{time.Now().Format(time.RFC3339)}, args...)...)
	_ = os.MkdirAll(ConfigDir(), 0o755)
	f, err := os.OpenFile(debugFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(msg)
}

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

// gauge renders a 10-cell bar for a 0-100 percentage using eighth-block partials
// (█▏▎▍▌▋▊▉) so a single cell shows sub-10% resolution — far smoother than
// whole-cell fills. Empty cells use ░.
func gauge(pct int) string {
	const w = 10
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	eighths := pct * w * 8 / 100 // total eighth-columns filled
	full := eighths / 8
	rem := eighths % 8
	partials := []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
	var b strings.Builder
	for i := 0; i < w; i++ {
		switch {
		case i < full:
			b.WriteString("█")
		case i == full && rem > 0:
			b.WriteString(partials[rem])
		default:
			b.WriteString("░")
		}
	}
	return b.String()
}

// pctColor returns green/yellow/red for a usage percentage.
func pctColor(p int) string {
	switch {
	case p >= 90:
		return red
	case p >= 75:
		return yellow
	default:
		return green
	}
}

// costColor flags session spend: green under $5, yellow under $20, red beyond.
func costColor(usd float64) string {
	switch {
	case usd >= 20:
		return red
	case usd >= 5:
		return yellow
	default:
		return green
	}
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
