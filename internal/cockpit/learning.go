package cockpit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxRecentApplied = 12

// learningStore persists operator preferences across sessions (not deleted on cleanup).
type learningStore struct {
	Accepted      map[string]int    `json:"accepted"`       // category -> apply count
	Explicit      map[string]string `json:"explicit"`       // key -> prefer|avoid
	RecentApplied []appliedEntry    `json:"recent_applied"` // newest first
}

type appliedEntry struct {
	At         string `json:"at"`
	Category   string `json:"category"`
	Suggestion string `json:"suggestion"`
	Project    string `json:"project"`
}

func learningFile() string { return filepath.Join(ConfigDir(), ".cockpit-learning.json") }

func loadLearning() learningStore {
	b, err := os.ReadFile(learningFile())
	if err != nil {
		return learningStore{
			Accepted: map[string]int{},
			Explicit: map[string]string{},
		}
	}
	var s learningStore
	if json.Unmarshal(b, &s) != nil {
		return learningStore{
			Accepted: map[string]int{},
			Explicit: map[string]string{},
		}
	}
	if s.Accepted == nil {
		s.Accepted = map[string]int{}
	}
	if s.Explicit == nil {
		s.Explicit = map[string]string{}
	}
	return s
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

// recordApplied learns from an accepted suggestion.
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
	_ = saveLearning(s)
}

// SetPrefer sets an explicit prefer/avoid for a lever category.
func SetPrefer(category, mode string) error {
	return setExplicitPreference(category, mode)
}

// ClearPrefer removes an explicit preference for a category.
func ClearPrefer(category string) error {
	return clearExplicitPreference(category)
}

// setExplicitPreference sets prefer or avoid for a lever category.
func setExplicitPreference(category, mode string) error {
	cat := normalizeCategory(category)
	if cat == "" {
		return fmt.Errorf("unknown category %q (try: graphify, delegation, mcp, loop, model, context, skill, workflow)", category)
	}
	if mode != "prefer" && mode != "avoid" {
		return fmt.Errorf("mode must be prefer or avoid")
	}
	s := loadLearning()
	s.Explicit[cat] = mode
	return saveLearning(s)
}

func clearExplicitPreference(category string) error {
	cat := normalizeCategory(category)
	if cat == "" {
		return fmt.Errorf("unknown category %q", category)
	}
	s := loadLearning()
	delete(s.Explicit, cat)
	return saveLearning(s)
}

func formatLearningForSignals() string {
	s := loadLearning()
	if len(s.Accepted) == 0 && len(s.Explicit) == 0 && len(s.RecentApplied) == 0 {
		return "none yet"
	}
	var parts []string

	if len(s.Accepted) > 0 {
		type kv struct {
			k string
			v int
		}
		ranked := make([]kv, 0, len(s.Accepted))
		for k, v := range s.Accepted {
			ranked = append(ranked, kv{k, v})
		}
		sort.Slice(ranked, func(i, j int) bool {
			if ranked[i].v == ranked[j].v {
				return ranked[i].k < ranked[j].k
			}
			return ranked[i].v > ranked[j].v
		})
		accepts := make([]string, 0, len(ranked))
		for _, r := range ranked {
			accepts = append(accepts, fmt.Sprintf("%s:%d", r.k, r.v))
		}
		parts = append(parts, "accepted_levers="+strings.Join(accepts, " "))
	}

	if len(s.Explicit) > 0 {
		keys := make([]string, 0, len(s.Explicit))
		for k := range s.Explicit {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		explicit := make([]string, 0, len(keys))
		for _, k := range keys {
			explicit = append(explicit, k+":"+s.Explicit[k])
		}
		parts = append(parts, "explicit="+strings.Join(explicit, " "))
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

// RunPrefs prints the learned operator profile.
func RunPrefs(w interface{ Write([]byte) (int, error) }) {
	s := loadLearning()
	fmt.Fprintln(w, "Cockpit learning profile (~/.claude/.cockpit-learning.json)")
	fmt.Fprintln(w)

	if len(s.Accepted) == 0 && len(s.Explicit) == 0 && len(s.RecentApplied) == 0 {
		fmt.Fprintln(w, "No preferences learned yet. Apply suggestions with cockpit apply <n> to teach the advisor.")
		return
	}

	if len(s.Accepted) > 0 {
		fmt.Fprintln(w, "Accepted levers (apply count):")
		type kv struct {
			k string
			v int
		}
		ranked := make([]kv, 0, len(s.Accepted))
		for k, v := range s.Accepted {
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

	if len(s.Explicit) > 0 {
		fmt.Fprintln(w, "Explicit preferences:")
		keys := make([]string, 0, len(s.Explicit))
		for k := range s.Explicit {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "  %s  %s\n", k, s.Explicit[k])
		}
		fmt.Fprintln(w)
	}

	if len(s.RecentApplied) > 0 {
		fmt.Fprintln(w, "Recently applied:")
		for _, e := range s.RecentApplied {
			fmt.Fprintf(w, "  %s  [%s]  %s\n", e.At[:10], e.Category, truncate(e.Suggestion, 72))
		}
	}
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
