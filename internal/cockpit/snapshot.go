package cockpit

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
)

// cockpitSnapshot bridges analyze → statusline (phase, plan, memos) for ONE
// session. Session/Cwd stamp the file so terminal commands can resolve which
// session they belong to; ctx/cost fields carry the session's own instruments
// so classification never reads another session's context pressure.
type cockpitSnapshot struct {
	Session            string  `json:"session,omitempty"`
	Cwd                string  `json:"cwd,omitempty"`
	Phase              string  `json:"phase"`
	CostIndex          string  `json:"cost_index"`
	ContextUsedPct     int     `json:"context_used_pct"`
	CtxSize            int64   `json:"ctx_size,omitempty"`
	CtxTokens          int64   `json:"ctx_tokens,omitempty"`
	CostUSD            float64 `json:"cost_usd,omitempty"`
	Rate5hPct          int     `json:"rate_5h_pct"`
	Rate7dPct          int     `json:"rate_7d_pct"`
	Searches           int     `json:"searches"`
	GraphifyGraph      bool    `json:"graphify_graph"`
	ToolTop            string  `json:"tool_top"`
	PlanAnchor         string  `json:"plan_anchor"`
	PlanDeviation      string  `json:"plan_deviation"`
	PendingSuggestions int     `json:"pending_suggestions"`
	AdvisorOK          bool    `json:"advisor_ok"`
}

func writeSnapshot(session string, s cockpitSnapshot) {
	if session == "" {
		return
	}
	s.Session = session
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	_ = os.WriteFile(sessionSnapshotFile(session), b, 0o644)
}

func readSnapshot(session string) cockpitSnapshot {
	if session == "" {
		return cockpitSnapshot{CostIndex: costIndex()}
	}
	b, err := os.ReadFile(sessionSnapshotFile(session))
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

func buildSnapshot(s Signals, prReview, session, cwd string) cockpitSnapshot {
	phase := detectPhase(s, prReview)
	anchor, deviation := inferPlan(s.RecentPrompts)
	return cockpitSnapshot{
		Session:            session,
		Cwd:                cwd,
		Phase:              string(phase),
		CostIndex:          costIndex(),
		ContextUsedPct:     s.ContextUsedPct,
		Rate5hPct:          s.Rate5hPct,
		Rate7dPct:          s.Rate7dPct,
		Searches:           s.Searches,
		GraphifyGraph:      s.GraphifyGraph,
		ToolTop:            topTools(s.ToolHistogram, 3),
		PlanAnchor:         anchor,
		PlanDeviation:      deviation,
		PendingSuggestions: len(readSuggestions(session)),
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

// maybeChime rings the terminal bell once when this session's context crosses
// into WARN. Chime state is per-session so a concurrent session's context level
// cannot suppress (or double-fire) this one's alert.
func maybeChime(session string, ctxPct int) string {
	if os.Getenv("COCKPIT_ALERT_CHIME") != "1" || session == "" {
		return ""
	}
	prev := 0
	if b, err := os.ReadFile(sessionChimeFile(session)); err == nil {
		prev, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	_ = os.WriteFile(sessionChimeFile(session), []byte(strconv.Itoa(ctxPct)), 0o644)
	if prev < 90 && ctxPct >= 90 {
		return "\a"
	}
	return ""
}
