package cockpit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// cockpitSnapshot bridges analyze → statusline (phase, plan, memos).
type cockpitSnapshot struct {
	Phase              string `json:"phase"`
	CostIndex          string `json:"cost_index"`
	ContextUsedPct     int    `json:"context_used_pct"`
	Rate5hPct          int    `json:"rate_5h_pct"`
	Searches           int    `json:"searches"`
	GraphifyGraph      bool   `json:"graphify_graph"`
	ToolTop            string `json:"tool_top"`
	PlanAnchor         string `json:"plan_anchor"`
	PlanDeviation      string `json:"plan_deviation"`
	PendingSuggestions int    `json:"pending_suggestions"`
	AdvisorOK          bool   `json:"advisor_ok"`
}

func snapshotFile() string   { return filepath.Join(ConfigDir(), ".cockpit-snapshot") }
func chimeStateFile() string { return filepath.Join(ConfigDir(), ".cockpit-chime-state") }

func writeSnapshot(s cockpitSnapshot) {
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	_ = os.WriteFile(snapshotFile(), b, 0o644)
}

func readSnapshot() cockpitSnapshot {
	b, err := os.ReadFile(snapshotFile())
	if err != nil {
		return cockpitSnapshot{CostIndex: costIndex()}
	}
	var s cockpitSnapshot
	if json.Unmarshal(b, &s) != nil {
		return cockpitSnapshot{CostIndex: costIndex()}
	}
	if s.CostIndex == "" {
		s.CostIndex = costIndex()
	}
	return s
}

func buildSnapshot(s Signals, prReview string) cockpitSnapshot {
	phase := detectPhase(s, prReview)
	anchor, deviation := inferPlan(s.RecentPrompts)
	return cockpitSnapshot{
		Phase:              string(phase),
		CostIndex:          costIndex(),
		ContextUsedPct:     s.ContextUsedPct,
		Rate5hPct:          s.Rate5hPct,
		Searches:           s.Searches,
		GraphifyGraph:      s.GraphifyGraph,
		ToolTop:            topTools(s.ToolHistogram, 3),
		PlanAnchor:         anchor,
		PlanDeviation:      deviation,
		PendingSuggestions: len(readSuggestions()),
		AdvisorOK:          true,
	}
}

func topTools(h map[string]int, n int) string {
	if len(h) == 0 {
		return "none"
	}
	type kv struct {
		k string
		v int
	}
	ranked := make([]kv, 0, len(h))
	for k, v := range h {
		ranked = append(ranked, kv{k, v})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].v == ranked[j].v {
			return ranked[i].k < ranked[j].k
		}
		return ranked[i].v > ranked[j].v
	})
	if len(ranked) > n {
		ranked = ranked[:n]
	}
	parts := make([]string, len(ranked))
	for i, r := range ranked {
		parts[i] = r.k + ":" + strconv.Itoa(r.v)
	}
	return strings.Join(parts, " ")
}

// maybeChime rings the terminal bell once when context crosses into WARN.
func maybeChime(ctxPct int) string {
	if os.Getenv("COCKPIT_ALERT_CHIME") != "1" {
		return ""
	}
	prev := 0
	if b, err := os.ReadFile(chimeStateFile()); err == nil {
		prev, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	_ = os.WriteFile(chimeStateFile(), []byte(strconv.Itoa(ctxPct)), 0o644)
	if prev < 90 && ctxPct >= 90 {
		return "\a"
	}
	return ""
}
