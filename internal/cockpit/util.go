package cockpit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Catppuccin Mocha (dark) — a balanced 24-bit palette designed to be easy on the eyes
// on dark backgrounds: rich, harmonious hues rather than harsh saturation, and a
// soft (not washed-out) secondary for labels/separators.
const (
	darkGreen   = "\033[38;2;166;227;161m" // Mocha Green
	darkYellow  = "\033[38;2;249;226;175m" // Mocha Yellow
	darkRed     = "\033[38;2;243;139;168m" // Mocha Red
	darkCyan    = "\033[38;2;148;226;213m" // Mocha Teal
	darkBlue    = "\033[38;2;137;180;250m" // Mocha Blue
	darkMagenta = "\033[38;2;203;166;247m" // Mocha Mauve
	darkDim     = "\033[38;2;147;153;178m" // Mocha Overlay2 — soft, readable secondary
)

// Catppuccin Latte (light) — high contrast colors for light backgrounds
const (
	lightGreen   = "\033[38;2;64;160;43m"   // Latte Green
	lightYellow  = "\033[38;2;175;109;0m"   // Latte Yellow
	lightRed     = "\033[38;2;210;15;57m"   // Latte Red
	lightCyan    = "\033[38;2;23;146;153m"  // Latte Teal
	lightBlue    = "\033[38;2;30;102;205m"  // Latte Blue
	lightMagenta = "\033[38;2;136;23;152m"  // Latte Mauve
	lightDim     = "\033[38;2;108;115;137m" // Latte Text — darker secondary
)

const (
	rst  = "\033[0m"
	bold = "\033[1m"
)

var (
	green   string
	yellow  string
	red     string
	cyan    string
	blue    string
	magenta string
	dim     string
)

func init() {
	if isLightMode() {
		green, yellow, red, cyan, blue, magenta, dim = lightGreen, lightYellow, lightRed, lightCyan, lightBlue, lightMagenta, lightDim
	} else {
		green, yellow, red, cyan, blue, magenta, dim = darkGreen, darkYellow, darkRed, darkCyan, darkBlue, darkMagenta, darkDim
	}
}

// isLightMode detects if the terminal has a light background.
// Checks COCKPIT_LIGHT_MODE env var first, then COLORFGBG (last digit: 7=light, 0=dark).
func isLightMode() bool {
	if m := os.Getenv("COCKPIT_LIGHT_MODE"); m != "" {
		return m == "1" || m == "true" || m == "yes"
	}
	// COLORFGBG format: "foreground;background" (e.g. "0;7" for black text on white)
	if cfgbg := os.Getenv("COLORFGBG"); cfgbg != "" {
		parts := strings.Split(cfgbg, ";")
		if len(parts) >= 2 {
			return parts[1] == "7" || parts[1] == "15" // 7=white, 15=bright white
		}
	}
	return false
}

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

// cockpitDir is where all cockpit state, logs, and job artifacts live, keeping
// ~/.claude itself uncluttered. Honors CLAUDE_CONFIG_DIR via ConfigDir().
func cockpitDir() string {
	d := filepath.Join(ConfigDir(), "cockpit-logs")
	_ = os.MkdirAll(d, 0o755)
	return d
}

func debugFile() string { return filepath.Join(cockpitDir(), ".cockpit-debug.log") }

// legacy pre-session-isolation global files — no longer read or written, removed
// by cleanup/uninstall so a stale report from another session cannot resurface.
func legacyHintFile() string   { return filepath.Join(cockpitDir(), ".model-hint") }
func legacyReportFile() string { return filepath.Join(cockpitDir(), ".session-report") }
func legacyGlobalFiles() []string {
	return []string{
		legacyHintFile(), legacyReportFile(),
		filepath.Join(cockpitDir(), ".cockpit-snapshot"),
		filepath.Join(cockpitDir(), ".cockpit-chime-state"),
	}
}

// sessionStamp is the identity header every per-session JSON artifact carries so
// terminal commands can find "their" session by project directory.
type sessionStamp struct {
	Session string `json:"session"`
	Cwd     string `json:"cwd"`
}

// resolveSession identifies which session a terminal command (list, apply,
// status, plan, debrief) should read, since the Bash environment carries no
// session id. Order: explicit COCKPIT_SESSION, the harness's CLAUDE_SESSION_ID
// if present, else the freshest session artifact stamped with this cwd. When
// nothing matches the cwd, another project's session is used only if it is the
// single fresh session at all — with several live sessions, cross-project reads
// stay isolated and return nothing rather than someone else's suggestions.
func resolveSession(cwd string) string {
	if s := os.Getenv("COCKPIT_SESSION"); s != "" {
		return s
	}
	if s := os.Getenv("CLAUDE_SESSION_ID"); s != "" {
		return s
	}
	type cand struct {
		session, cwd string
		mod          time.Time
	}
	newest := map[string]cand{} // session -> freshest stamp
	for _, pat := range []string{"*.report", "*.snapshot"} {
		matches, _ := filepath.Glob(filepath.Join(cockpitDir(), pat))
		for _, p := range matches {
			info, err := os.Stat(p)
			if err != nil || time.Since(info.ModTime()) > hintMaxAge {
				continue
			}
			st, ok := readSessionStamp(p)
			if !ok || st.Session == "" {
				continue
			}
			if prev, seen := newest[st.Session]; !seen || info.ModTime().After(prev.mod) {
				newest[st.Session] = cand{st.Session, st.Cwd, info.ModTime()}
			}
		}
	}
	var best cand
	for _, c := range newest {
		if c.cwd == cwd && c.mod.After(best.mod) {
			best = c
		}
	}
	if best.session != "" {
		return best.session
	}
	if len(newest) == 1 {
		for _, c := range newest {
			return c.session
		}
	}
	return ""
}

func readSessionStamp(path string) (sessionStamp, bool) {
	f, err := os.Open(path)
	if err != nil {
		return sessionStamp{}, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxHintBytes))
	if err != nil {
		return sessionStamp{}, false
	}
	var st sessionStamp
	if json.Unmarshal(b, &st) != nil {
		return sessionStamp{}, false
	}
	return st, true
}

// stateFile holds the authoritative context/cost/rate snapshot the status line
// receives from Claude Code, so the Stop-hook analyzer (which is not given that
// data) can read the real context window instead of guessing from the model name.
// It survives SessionEnd (see RunCleanup) so a new session shows last-known
// 5h/7d/context immediately instead of resetting to 0.
func stateFile() string { return filepath.Join(cockpitDir(), ".cockpit-state") }

func debugLog(format string, args ...any) {
	if os.Getenv("COCKPIT_DEBUG") != "1" {
		return
	}
	msg := fmt.Sprintf("%s "+format+"\n", append([]any{time.Now().Format(time.RFC3339)}, args...)...)
	_ = os.MkdirAll(cockpitDir(), 0o755)
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

// displayWidth approximates terminal columns for s: emoji and wide symbols count
// as 2, combining marks (variation selector, ZWJ) as 0, everything else as 1.
// Keeps wrapping from clipping lines that start with a 2-column emoji.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r == 0xFE0F || r == 0x200D || (r >= 0x0300 && r <= 0x036F):
			// zero-width: variation selector, ZWJ, combining marks
		case r >= 0x1F000 || (r >= 0x2190 && r <= 0x2BFF) || (r >= 0x2300 && r <= 0x27BF):
			w += 2 // emoji, arrows, symbols
		default:
			w++
		}
	}
	return w
}

// stripANSI removes SGR ("\033[...m") escape sequences so a colored segment can
// be measured by its visible width.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// visibleWidth is the terminal-column width of a colored string, ignoring escapes.
func visibleWidth(s string) int { return displayWidth(stripANSI(s)) }

// barSeg is one field of an instrument row. text is already colored and
// rst-terminated; prio orders dropping when the row is too wide for the terminal
// (lowest prio drops first). Use prioPinned to keep a segment at any width.
type barSeg struct {
	text string
	prio int
}

const prioPinned = 1 << 20

// assembleRow joins segments with the " · " separator, dropping the
// lowest-priority optional segments until the visible width fits cols. Segments
// are dropped whole (never cut mid-escape) and pinned segments always survive.
// Original left-to-right order is preserved in the output.
func assembleRow(segs []barSeg, cols int) string {
	const sepW = 3 // visible width of " · "
	sep := dim + " · " + rst
	in := make([]bool, len(segs))
	w := make([]int, len(segs))
	for i, s := range segs {
		in[i] = true
		w[i] = visibleWidth(s.text)
	}
	width := func() int {
		total, n := 0, 0
		for i := range segs {
			if in[i] {
				total += w[i]
				n++
			}
		}
		if n > 1 {
			total += (n - 1) * sepW
		}
		return total
	}
	for width() > cols {
		lowest, at := prioPinned, -1
		for i, s := range segs {
			if in[i] && s.prio < lowest {
				lowest, at = s.prio, i
			}
		}
		if at < 0 || lowest >= prioPinned {
			break // only pinned segments remain
		}
		in[at] = false
	}
	var parts []string
	for i, s := range segs {
		if in[i] {
			parts = append(parts, s.text)
		}
	}
	return strings.Join(parts, sep)
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
		if strings.HasSuffix(line, ":") {
			continue // emoji-led header/preamble (e.g. "✅ Two quick levers:") => drop it
		}
		res = append(res, line)
		if len(res) >= max {
			break
		}
	}
	return res
}

// advisorLines keeps emoji-led suggestion lines, allowing an optional WARN|/CAUT| prefix.
func advisorLines(out string, max int) []string {
	var res []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.ReplaceAll(line, "**", "")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "TOOLGAP:") {
			continue
		}
		body := line
		if _, b, ok := parseSeverityPrefix(line); ok {
			body = b
		}
		r, _ := utf8.DecodeRuneInString(body)
		if r == utf8.RuneError || r < 0x80 {
			continue
		}
		if strings.HasSuffix(body, ":") {
			continue
		}
		res = append(res, line)
		if len(res) >= max {
			break
		}
	}
	return res
}
