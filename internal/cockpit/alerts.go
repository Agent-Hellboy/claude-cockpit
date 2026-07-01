package cockpit

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// AlertLevel mirrors EICAS/ECAM crew alerting severity.
type AlertLevel int

const (
	AlertWarn AlertLevel = iota
	AlertCaut
	AlertAdv
	AlertMemo
)

func (l AlertLevel) String() string {
	switch l {
	case AlertWarn:
		return "WARN"
	case AlertCaut:
		return "CAUT"
	case AlertAdv:
		return "ADV"
	default:
		return "MEMO"
	}
}

// Display returns a short, non-aviation label for the status bar.
func (l AlertLevel) Display() string {
	switch l {
	case AlertWarn:
		return "high"
	case AlertCaut:
		return "watch"
	case AlertAdv:
		return "tip"
	default:
		return "note"
	}
}

func alertColor(l AlertLevel) string {
	switch l {
	case AlertWarn:
		return red
	case AlertCaut:
		return yellow
	case AlertAdv:
		return cyan
	default:
		return dim
	}
}

// reverse is the SGR for swapped fg/bg — turns a colored label into a filled chip.
const reverse = "\033[7m"

// alertChip renders the severity label so urgency reads at a glance: WARN and
// CAUT become filled reverse-video chips (impossible to miss), while advisories
// and memos stay as quiet colored text so they don't compete for attention.
func alertChip(l AlertLevel) string {
	label := l.Display()
	switch l {
	case AlertWarn, AlertCaut:
		return reverse + alertColor(l) + bold + " " + label + " " + rst
	default:
		return alertColor(l) + label + rst
	}
}

type classifiedSuggestion struct {
	Level AlertLevel
	Text  string
}

// classifySuggestion assigns EICAS-style severity from instruments + text.
func classifySuggestion(text string, snap cockpitSnapshot, st cockpitState) classifiedSuggestion {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return classifiedSuggestion{Level: AlertMemo, Text: raw}
	}
	if lvl, body, ok := parseSeverityPrefix(raw); ok {
		return classifiedSuggestion{Level: lvl, Text: body}
	}

	lower := strings.ToLower(raw)
	ctx := snap.ContextUsedPct
	if ctx == 0 && st.CtxPct > 0 {
		ctx = st.CtxPct
	}
	rate := snap.Rate5hPct
	if rate == 0 {
		rate = st.FiveH
	}

	switch {
	case ctx >= 90 || rate >= 90 || strings.Contains(lower, "/compact") && ctx >= 75:
		return classifiedSuggestion{Level: AlertWarn, Text: raw}
	case ctx >= 70 || rate >= 75 || snap.Searches >= 15 || strings.Contains(lower, "re-read"):
		return classifiedSuggestion{Level: AlertCaut, Text: raw}
	case strings.HasPrefix(raw, "✅") || strings.Contains(lower, "efficient"):
		return classifiedSuggestion{Level: AlertMemo, Text: raw}
	default:
		return classifiedSuggestion{Level: AlertAdv, Text: raw}
	}
}

func parseSeverityPrefix(s string) (AlertLevel, string, bool) {
	if i := strings.Index(s, "|"); i > 0 {
		switch strings.ToUpper(strings.TrimSpace(s[:i])) {
		case "WARN", "WARNING":
			return AlertWarn, strings.TrimSpace(s[i+1:]), true
		case "CAUT", "CAUTION":
			return AlertCaut, strings.TrimSpace(s[i+1:]), true
		case "ADV", "ADVISORY":
			return AlertAdv, strings.TrimSpace(s[i+1:]), true
		case "MEMO":
			return AlertMemo, strings.TrimSpace(s[i+1:]), true
		}
	}
	return AlertAdv, s, false
}

func parseSuggestionStore(raw []string, snap cockpitSnapshot, st cockpitState) []classifiedSuggestion {
	out := make([]classifiedSuggestion, 0, len(raw))
	for _, ln := range raw {
		out = append(out, classifySuggestion(ln, snap, st))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Level < out[j].Level })
	return out
}

// partitionSuggestions splits informational notes from fixes cockpit apply can wire up.
func partitionSuggestions(hints []classifiedSuggestion) (notes, fixes []classifiedSuggestion) {
	for _, h := range hints {
		if isApplyable(h) {
			fixes = append(fixes, h)
		} else {
			notes = append(notes, h)
		}
	}
	return notes, fixes
}

// isApplyable reports whether cockpit apply should offer this line (MCP, skills, CLAUDE.md rules).
// Slash-command reminders and efficiency memos are shown but not numbered for apply.
func isApplyable(c classifiedSuggestion) bool {
	if c.Level == AlertMemo {
		return false
	}
	lower := strings.ToLower(c.Text)
	nonApply := []string{
		"efficient", "nominal", "looks good", "already efficient",
		"reversionary advisor", "instruments nominal",
		"/compact", "/context", "/clear",
		"run /model", "switch /model", "switch to a cheaper model",
		"switch /model down", "rate limit hot", "context critical",
		"context high — run /context", "context high — consider /compact",
	}
	for _, p := range nonApply {
		if strings.Contains(lower, p) {
			return false
		}
	}
	if c.Level == AlertAdv {
		return true
	}
	applySignals := []string{
		"mcp", "skill", "graphify", "claude.md", "install", "playwright",
		"integration", "audit ", "subagent", "/loop", ".claude",
	}
	for _, p := range applySignals {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func countApplyable(hints []classifiedSuggestion) int {
	n := 0
	for _, h := range hints {
		if isApplyable(h) {
			n++
		}
	}
	return n
}

// ChecklistSteps returns ECAM-style corrective actions for a warning topic.
func ChecklistSteps(topic string) []string {
	switch strings.ToLower(topic) {
	case "context", "ctx", "compact":
		return []string{
			"Run /context to see what is filling the window",
			"Run /compact to summarize older turns",
			"Run /clear if you are switching to unrelated work",
		}
	case "budget", "rate", "model", "cost":
		return []string{
			"Run /model to switch to a cheaper tier (Haiku/Sonnet)",
			"Delegate broad reads to an Explore subagent",
			"Pause non-essential tool loops until rate cools",
		}
	case "search", "graphify", "grep":
		return []string{
			"Run graphify query for architecture questions",
			"Stop repeated grep/find on the same paths",
			"Build graph with /graphify . if graphify-out is missing",
		}
	default:
		return []string{
			"Run cockpit list to see numbered suggestions",
			"Run cockpit checklist <topic> for a focused procedure",
			"Run cockpit systems to inspect integrations",
		}
	}
}

// RunChecklist prints an ECAM-style procedure.
func RunChecklist(w io.Writer, topic string) {
	steps := ChecklistSteps(topic)
	fmt.Fprintf(w, "Cockpit checklist — %s\n\n", fallback(topic, "general"))
	for i, s := range steps {
		fmt.Fprintf(w, "  %d. %s\n", i+1, s)
	}
}

// ruleBasedSuggestions is reversionary mode: deterministic hints when the advisor fails.
func ruleBasedSuggestions(sig string) []classifiedSuggestion {
	var out []classifiedSuggestion
	ctx := parseSignalInt(sig, "context_used_pct")
	rate5 := parseSignalInt(sig, "rate_5h_pct")
	searches := parseSignalInt(sig, "searches")
	graph := strings.Contains(sig, "graphify_graph=yes")

	switch {
	case ctx >= 90:
		out = append(out, classifiedSuggestion{
			Level: AlertWarn,
			Text:  "⚠️ Context critical — run /context then /compact before the next large read",
		})
	case ctx >= 75:
		out = append(out, classifiedSuggestion{
			Level: AlertCaut,
			Text:  "📦 Context high — consider /compact or delegating broad reads to Explore",
		})
	}
	if rate5 >= 85 {
		out = append(out, classifiedSuggestion{
			Level: AlertWarn,
			Text:  "⛽ Rate limit hot — switch /model down or use Haiku subagents",
		})
	}
	if searches >= 10 && !graph {
		out = append(out, classifiedSuggestion{
			Level: AlertCaut,
			Text:  "🔍 Heavy search pattern — audit /graphify . or use graphify query",
		})
	}
	if len(out) == 0 {
		out = append(out, classifiedSuggestion{
			Level: AlertMemo,
			Text:  "✅ Instruments nominal — reversionary advisor (haiku unavailable)",
		})
	}
	return out
}

func parseSignalInt(sig, key string) int {
	i := strings.Index(sig, key+"=")
	if i < 0 {
		return 0
	}
	rest := sig[i+len(key)+1:]
	end := len(rest)
	for j, r := range rest {
		if r == ' ' || r == '\n' || r == '(' || r == '%' {
			end = j
			break
		}
	}
	v, _ := strconv.Atoi(strings.TrimSpace(rest[:end]))
	return v
}

func writeSuggestionReport(lines []classifiedSuggestion) error {
	if len(lines) == 0 {
		return nil
	}
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = l.Level.String() + "|" + l.Text
	}
	if err := os.WriteFile(reportFile(), []byte(strings.Join(texts, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(hintFile(), []byte(lines[0].Text), 0o644)
}
