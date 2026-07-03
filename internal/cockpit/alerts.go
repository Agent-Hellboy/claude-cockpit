package cockpit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
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

// noteMaxAge is how long a non-applyable instrument note stays presentable.
// Notes describe a moment ("15 searches so far, rate climbing"); the worker
// rewrites them on every advisor run, so the report's age IS the notes' age.
// Standing applyable fixes are exempt — they persist until applied.
const noteMaxAge = 30 * time.Minute

func parseSuggestionStore(raw []string, age time.Duration, snap cockpitSnapshot, st cockpitState) []classifiedSuggestion {
	out := make([]classifiedSuggestion, 0, len(raw))
	for _, ln := range raw {
		c := classifySuggestion(ln, snap, st)
		if staleInstrumentClaim(c.Text, snap) {
			continue
		}
		if age > noteMaxAge && !isApplyable(c) {
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Level < out[j].Level })
	return out
}

var pctClaimRe = regexp.MustCompile(`(\d{1,3})%`)

// claimTolerance is how far a cited percentage may drift from the live gauge
// before the suggestion no longer describes this world.
const claimTolerance = 20

// staleInstrumentClaim reports whether a stored suggestion cites instrument
// pressure that today's gauges contradict — e.g. "5-hour budget at 97%" (or
// "rate climbing to 73% in 5h") after the window reset to 3%. The rule is
// disagreement, not absolute level: a claim ≥50% that misses the live gauge by
// more than claimTolerance in either direction is advisor output from an
// earlier state of the world, and showing it next to fresh gauges reads as a
// broken bar. The snapshot is authoritative because the statusline re-patches
// it on every render.
func staleInstrumentClaim(text string, snap cockpitSnapshot) bool {
	lower := strings.ToLower(text)
	m := pctClaimRe.FindStringSubmatch(lower)
	if m == nil {
		return false
	}
	claimed, _ := strconv.Atoi(m[1])
	if claimed < 50 {
		return false // low percentages are rhetoric ("30% cheaper"), not pressure
	}
	off := func(actual int) bool {
		d := claimed - actual
		return d > claimTolerance || d < -claimTolerance
	}
	switch {
	case strings.Contains(lower, "5-hour") || strings.Contains(lower, "5h"):
		return off(snap.Rate5hPct)
	case strings.Contains(lower, "7-day") || strings.Contains(lower, "7d"):
		return off(snap.Rate7dPct)
	case strings.Contains(lower, "budget") || strings.Contains(lower, "rate"):
		// unspecified window: live if EITHER gauge matches the claim.
		return off(snap.Rate5hPct) && off(snap.Rate7dPct)
	case strings.Contains(lower, "context") || strings.Contains(lower, "ctx"):
		return off(snap.ContextUsedPct)
	}
	return false
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
		// budget/rate pressure is an instrument condition, not a wireable fix:
		// it must regenerate fresh each advisor run and vanish when the window
		// resets, never persist as a standing suggestion.
		"5-hour", "5h budget", "budget at", "near the limit", "hit the ceiling",
		"rate limit", "wrapping up",
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
	case "faults", "fault", "errors":
		return []string{
			"Verify paths with Glob/ls before Read/Edit — most faults are not-found",
			"Locate symbols with graphify query or LSP instead of guessing file paths",
			"Read a file before editing it; retry Edit with exact current text",
			"If one tool dominates errors, change approach rather than re-running it",
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
	if nf := parseSignalInt(sig, "not_found_errors"); nf >= 5 {
		out = append(out, classifiedSuggestion{
			Level: AlertCaut,
			Text:  "📁 Repeated not-found tool faults — verify paths with Glob or graphify query before Read/Edit",
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

// suggestionReport is the per-session suggestion store. The session/cwd stamp
// is what lets `cockpit list`/`apply` in a terminal find the right session, and
// what keeps one session's advice out of another session's status bar.
type suggestionReport struct {
	sessionStamp
	Lines []string `json:"lines"`
}

func writeSuggestionReport(session, cwd string, lines []classifiedSuggestion) error {
	if len(lines) == 0 || session == "" {
		return nil
	}
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = l.Level.String() + "|" + l.Text
	}
	return writeReportLines(session, cwd, texts)
}

func writeReportLines(session, cwd string, texts []string) error {
	b, err := json.Marshal(suggestionReport{sessionStamp{session, cwd}, texts})
	if err != nil {
		return err
	}
	return os.WriteFile(sessionReportFile(session), b, 0o644)
}

// ---- suggestion memory: stop the advisor nagging the same lever every run ----

const (
	maxSeenTexts   = 60 // per-session history cap
	maxReportLines = 4  // stored suggestion rows (mirrors the bar's safety cap)
)

// seenStore remembers every suggestion the advisor has surfaced this session —
// including ones the user applied or that rotated out — so a lever is proposed
// once, not on every advisor cadence.
type seenStore struct {
	Texts []string `json:"texts"`
}

func readSeen(session string) seenStore {
	var s seenStore
	if session == "" {
		return s
	}
	b, err := os.ReadFile(sessionSeenFile(session))
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

func appendSeen(session string, texts []string) {
	if session == "" || len(texts) == 0 {
		return
	}
	s := readSeen(session)
	s.Texts = append(s.Texts, texts...)
	if len(s.Texts) > maxSeenTexts {
		s.Texts = s.Texts[len(s.Texts)-maxSeenTexts:]
	}
	if b, err := json.Marshal(s); err == nil {
		_ = os.WriteFile(sessionSeenFile(session), b, 0o644)
	}
}

// leverTokens name the concrete controls a suggestion can pull. Two suggestions
// that mention the same lever set are the same advice however haiku rephrases
// them. Specific integration names come before generic categories so
// "Playwright MCP" and "Sentry MCP" key differently.
var leverTokens = []string{
	"graphify", "/loop", "/batch", "/verify", "/debug", "/code-review", "/model",
	"explore", "subagent", "plan mode", "playwright", "puppeteer", "context7",
	"sentry", "github", "jira", "figma", "slack", "notion", "postgres", "sqlite",
	"sniffly", "vibe-log", "ccusage", "opentelemetry", "signoz",
	"haiku", "sonnet", "opus", "mcp", "skill", "claude.md",
}

// leverKey normalizes a suggestion to the set of levers it pulls; falls back to
// the normalized text when no known lever is mentioned.
func leverKey(text string) string {
	lower := strings.ToLower(stripSeverityPrefix(text))
	var hits []string
	for _, tok := range leverTokens {
		if strings.Contains(lower, tok) {
			hits = append(hits, tok)
		}
	}
	if len(hits) == 0 {
		return "text:" + strings.Join(strings.Fields(lower), " ")
	}
	return strings.Join(hits, "+")
}

// mergeSuggestions folds a new advisor batch into the session's report:
//   - standing applyable fixes persist until applied (or rotated out),
//   - instrument notes/warnings are regenerated fresh each run,
//   - an applyable lever already suggested this session (seen store) is muted,
//
// and returns what was stored. The seen store records newly surfaced levers.
func mergeSuggestions(session, cwd string, incoming []classifiedSuggestion) []classifiedSuggestion {
	snap := readSnapshot(session)
	st, _ := readState()

	keys := map[string]bool{}
	for _, t := range readSeen(session).Texts {
		keys[leverKey(t)] = true
	}

	var out []classifiedSuggestion
	for _, ln := range readSuggestions(session) {
		c := classifySuggestion(ln, snap, st)
		if !isApplyable(c) || staleInstrumentClaim(c.Text, snap) {
			continue
		}
		out = append(out, c)
		keys[leverKey(c.Text)] = true
	}

	var fresh []string
	for _, c := range incoming {
		if isApplyable(c) {
			k := leverKey(c.Text)
			if keys[k] {
				continue
			}
			keys[k] = true
			fresh = append(fresh, c.Text)
		}
		out = append(out, c)
	}
	if len(out) > maxReportLines {
		// oldest standing fixes rotate out first; they stay in the seen store.
		out = out[len(out)-maxReportLines:]
	}
	appendSeen(session, fresh)
	if len(out) == 0 {
		_ = os.Remove(sessionReportFile(session))
		return nil
	}
	if err := writeSuggestionReport(session, cwd, out); err != nil {
		logf(session, "mergeSuggestions: write report: %v", err)
	}
	return out
}
