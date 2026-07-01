package cockpit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxRecentApplied = 12

// learningStore persists operator preferences across sessions (not deleted on cleanup).
type learningStore struct {
	Accepted      map[string]int `json:"accepted"`       // cockpit apply + strong signals
	Observed      map[string]int `json:"observed"`       // inferred from session behavior
	Ignored       map[string]int `json:"ignored"`        // suggestions shown but not acted on
	Cursors       map[string]int `json:"cursors"`        // session -> transcript entries processed
	RecentApplied []appliedEntry `json:"recent_applied"` // newest first
}

type appliedEntry struct {
	At         string `json:"at"`
	Category   string `json:"category"`
	Suggestion string `json:"suggestion"`
	Project    string `json:"project"`
}

type pendingSuggestions struct {
	Lines []string `json:"lines"`
}

func learningFile() string { return filepath.Join(ConfigDir(), ".cockpit-learning.json") }
func pendingFile() string  { return filepath.Join(ConfigDir(), ".cockpit-pending.json") }

func loadLearning() learningStore {
	b, err := os.ReadFile(learningFile())
	if err != nil {
		return freshLearning()
	}
	var s learningStore
	if json.Unmarshal(b, &s) != nil {
		return freshLearning()
	}
	if s.Accepted == nil {
		s.Accepted = map[string]int{}
	}
	if s.Observed == nil {
		s.Observed = map[string]int{}
	}
	if s.Ignored == nil {
		s.Ignored = map[string]int{}
	}
	if s.Cursors == nil {
		s.Cursors = map[string]int{}
	}
	return s
}

func freshLearning() learningStore {
	return learningStore{
		Accepted: map[string]int{},
		Observed: map[string]int{},
		Ignored:  map[string]int{},
		Cursors:  map[string]int{},
	}
}

func saveLearning(s learningStore) error {
	if err := os.MkdirAll(ConfigDir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(learningFile(), append(b, '\n'), 0o644)
}

// LearnFromSession scans new transcript activity and updates preferences automatically.
func LearnFromSession(transcriptPath, sessionID, project string) {
	if transcriptPath == "" || sessionID == "" {
		return
	}
	entries := tailEntries(transcriptPath, 5000)
	if len(entries) == 0 {
		return
	}
	key := sessionKey(sessionID)
	s := loadLearning()
	start := s.Cursors[key]
	if start > len(entries) {
		start = 0
	}
	if start >= len(entries) {
		return
	}
	changed := false
	for _, e := range entries[start:] {
		for _, cat := range observeEntry(e) {
			s.Observed[cat]++
			changed = true
		}
	}
	s.Cursors[key] = len(entries)
	if changed {
		_ = saveLearning(s)
	}
}

func observeEntry(e tEntry) []string {
	var cats []string
	role := e.Message.Role

	var items []contentItem
	if json.Unmarshal(e.Message.Content, &items) == nil {
		for _, it := range items {
			switch it.Type {
			case "tool_use":
				cats = append(cats, observeToolUse(it)...)
			case "text":
				if role == "user" {
					cats = append(cats, observeUserText(it.Text)...)
				}
			}
		}
		return uniqCategories(cats)
	}
	if role == "user" {
		var s string
		if json.Unmarshal(e.Message.Content, &s) == nil {
			return observeUserText(s)
		}
	}
	return nil
}

func observeToolUse(it contentItem) []string {
	var cats []string
	cmd := strings.ToLower(it.Input.Command)
	switch it.Name {
	case "Bash":
		cats = append(cats, observeCommand(cmd)...)
	case "Task":
		sub := strings.ToLower(it.Input.SubagentType)
		if sub == "explore" || strings.Contains(strings.ToLower(it.Input.Description), "explore") {
			cats = append(cats, "delegation")
		}
	case "Grep", "Glob", "SemanticSearch":
		// no automatic preference signal from search alone
	}
	return cats
}

func observeCommand(cmd string) []string {
	var cats []string
	switch {
	case strings.Contains(cmd, "graphify query") || strings.Contains(cmd, "graphify explain") || strings.Contains(cmd, "graphify path"):
		cats = append(cats, "graphify")
	case strings.Contains(cmd, "cockpit apply"):
		// apply is recorded separately; still counts as accepting workflow
		cats = append(cats, "workflow")
	case strings.Contains(cmd, "/loop") || strings.Contains(cmd, "cockpit loop"):
		cats = append(cats, "loop")
	}
	return cats
}

func observeUserText(text string) []string {
	lower := strings.ToLower(text)
	var cats []string
	switch {
	case strings.Contains(lower, "/model"):
		cats = append(cats, "model")
	case strings.Contains(lower, "/compact") || strings.Contains(lower, "/clear") || strings.Contains(lower, "/context"):
		cats = append(cats, "context")
	case strings.Contains(lower, "/loop"):
		cats = append(cats, "loop")
	case strings.Contains(lower, "cockpit apply"):
		cats = append(cats, "workflow")
	}
	return cats
}

func uniqCategories(cats []string) []string {
	if len(cats) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range cats {
		c = normalizeCategory(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// recordApplied learns from an accepted suggestion (cockpit apply).
func recordApplied(suggestion, project string) {
	s := loadLearning()
	cat := categorizeSuggestion(suggestion)
	s.Accepted[cat]++
	entry := appliedEntry{
		At:         time.Now().UTC().Format(time.RFC3339),
		Category:   cat,
		Suggestion: truncate(suggestion, 200),
		Project:    filepath.Base(project),
	}
	s.RecentApplied = append([]appliedEntry{entry}, s.RecentApplied...)
	if len(s.RecentApplied) > maxRecentApplied {
		s.RecentApplied = s.RecentApplied[:maxRecentApplied]
	}
	removePendingLine(suggestion)
	_ = saveLearning(s)
}

func recordIgnored(suggestion string) {
	cat := categorizeSuggestion(suggestion)
	if cat == "" {
		return
	}
	s := loadLearning()
	s.Ignored[cat]++
	_ = saveLearning(s)
}

// rotatePendingSuggestions marks prior suggestions as ignored when replaced.
func rotatePendingSuggestions(newLines []string) {
	old := loadPending()
	if len(old.Lines) == 0 {
		_ = savePending(pendingSuggestions{Lines: append([]string(nil), newLines...)})
		return
	}
	newSet := map[string]bool{}
	for _, ln := range newLines {
		newSet[ln] = true
	}
	for _, ln := range old.Lines {
		if !newSet[ln] {
			recordIgnored(ln)
		}
	}
	_ = savePending(pendingSuggestions{Lines: append([]string(nil), newLines...)})
}

func flushPendingSuggestions() {
	for _, ln := range loadPending().Lines {
		recordIgnored(ln)
	}
	_ = os.Remove(pendingFile())
}

func removePendingLine(suggestion string) {
	p := loadPending()
	if len(p.Lines) == 0 {
		return
	}
	var kept []string
	for _, ln := range p.Lines {
		if ln != suggestion {
			kept = append(kept, ln)
		}
	}
	_ = savePending(pendingSuggestions{Lines: kept})
}

func loadPending() pendingSuggestions {
	b, err := os.ReadFile(pendingFile())
	if err != nil {
		return pendingSuggestions{}
	}
	var p pendingSuggestions
	if json.Unmarshal(b, &p) != nil {
		return pendingSuggestions{}
	}
	return p
}

func savePending(p pendingSuggestions) error {
	if len(p.Lines) == 0 {
		return os.Remove(pendingFile())
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(pendingFile(), append(b, '\n'), 0o644)
}

func clearSessionCursor(sessionID string) {
	if sessionID == "" {
		return
	}
	s := loadLearning()
	delete(s.Cursors, sessionKey(sessionID))
	_ = saveLearning(s)
}

func formatLearningForSignals() string {
	s := loadLearning()
	if len(s.Accepted) == 0 && len(s.Observed) == 0 && len(s.Ignored) == 0 && len(s.RecentApplied) == 0 {
		return "none yet (learning is automatic from your session)"
	}
	var parts []string
	if block := formatCountBlock("accepted_levers", s.Accepted); block != "" {
		parts = append(parts, block)
	}
	if block := formatCountBlock("observed_levers", s.Observed); block != "" {
		parts = append(parts, block)
	}
	if block := formatCountBlock("ignored_levers", s.Ignored); block != "" {
		parts = append(parts, block)
	}
	if len(s.RecentApplied) > 0 {
		n := len(s.RecentApplied)
		if n > 4 {
			n = 4
		}
		recent := make([]string, 0, n)
		for _, e := range s.RecentApplied[:n] {
			recent = append(recent, e.Category)
		}
		parts = append(parts, "recent_applied="+strings.Join(recent, ","))
	}
	return strings.Join(parts, "  ")
}

func formatCountBlock(label string, counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	type kv struct {
		k string
		v int
	}
	ranked := make([]kv, 0, len(counts))
	for k, v := range counts {
		ranked = append(ranked, kv{k, v})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].v == ranked[j].v {
			return ranked[i].k < ranked[j].k
		}
		return ranked[i].v > ranked[j].v
	})
	parts := make([]string, 0, len(ranked))
	for _, r := range ranked {
		parts = append(parts, fmt.Sprintf("%s:%d", r.k, r.v))
	}
	return label + "=" + strings.Join(parts, " ")
}

// RunPrefs prints the automatically learned operator profile.
func RunPrefs(w io.Writer) {
	s := loadLearning()
	fmt.Fprintln(w, "Cockpit learning profile (~/.claude/.cockpit-learning.json)")
	fmt.Fprintln(w, "Learned automatically from what you do and which suggestions you skip.")
	fmt.Fprintln(w)

	if len(s.Accepted) == 0 && len(s.Observed) == 0 && len(s.Ignored) == 0 && len(s.RecentApplied) == 0 {
		fmt.Fprintln(w, "No preferences learned yet — keep working; cockpit observes your session.")
		return
	}

	printCountSection(w, "Accepted (cockpit apply)", s.Accepted)
	printCountSection(w, "Observed (from your actions)", s.Observed)
	printCountSection(w, "Ignored (suggestions not acted on)", s.Ignored)

	if len(s.RecentApplied) > 0 {
		fmt.Fprintln(w, "Recently applied:")
		for _, e := range s.RecentApplied {
			fmt.Fprintf(w, "  %s  [%s]  %s\n", e.At[:10], e.Category, truncate(e.Suggestion, 72))
		}
	}
}

func printCountSection(w io.Writer, title string, counts map[string]int) {
	if len(counts) == 0 {
		return
	}
	fmt.Fprintln(w, title+":")
	type kv struct {
		k string
		v int
	}
	ranked := make([]kv, 0, len(counts))
	for k, v := range counts {
		ranked = append(ranked, kv{k, v})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].v == ranked[j].v {
			return ranked[i].k < ranked[j].k
		}
		return ranked[i].v > ranked[j].v
	})
	for _, r := range ranked {
		fmt.Fprintf(w, "  %s  %d\n", r.k, r.v)
	}
	fmt.Fprintln(w)
}

func categorizeSuggestion(s string) string {
	return normalizeCategory(heuristicCategory(s))
}

func heuristicCategory(s string) string {
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "graphify"):
		return "graphify"
	case strings.Contains(lower, "explore") || strings.Contains(lower, "subagent") || strings.Contains(lower, "delegate"):
		return "delegation"
	case strings.Contains(lower, "mcp") || strings.Contains(s, "🔌"):
		return "mcp"
	case strings.Contains(lower, "/loop"):
		return "loop"
	case strings.Contains(lower, "sonnet") || strings.Contains(lower, "haiku") || strings.Contains(lower, "opus") || strings.Contains(lower, "model"):
		return "model"
	case strings.Contains(lower, "/compact") || strings.Contains(lower, "/clear") || strings.Contains(lower, "context"):
		return "context"
	case strings.Contains(lower, "skill"):
		return "skill"
	default:
		return "workflow"
	}
}

func normalizeCategory(cat string) string {
	cat = strings.ToLower(strings.TrimSpace(cat))
	switch cat {
	case "graphify", "graph", "graphify-out":
		return "graphify"
	case "delegation", "delegate", "explore", "subagent", "subagents", "agent", "agents":
		return "delegation"
	case "mcp", "integration", "integrations":
		return "mcp"
	case "loop", "polling", "ci":
		return "loop"
	case "model", "opus", "sonnet", "haiku":
		return "model"
	case "context", "compact", "clear":
		return "context"
	case "skill", "skills":
		return "skill"
	case "workflow", "work":
		return "workflow"
	default:
		return ""
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
