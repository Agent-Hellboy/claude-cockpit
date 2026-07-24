package cockpit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// inferPlan extracts a route anchor from early prompts and detects drift.
func inferPlan(prompts []string) (anchor, deviation string) {
	if len(prompts) == 0 {
		return "", ""
	}
	anchor = summarizePrompt(prompts[0])
	if len(prompts) < 4 {
		return anchor, ""
	}
	recent := strings.ToLower(strings.Join(prompts[len(prompts)-3:], " "))
	anchorLower := strings.ToLower(anchor)
	anchorWords := keywords(anchorLower)
	if len(anchorWords) == 0 {
		return anchor, ""
	}
	matches := 0
	for _, w := range anchorWords {
		if strings.Contains(recent, w) {
			matches++
		}
	}
	if matches*3 < len(anchorWords)*2 {
		deviation = "recent prompts diverge from session start"
	}
	return anchor, deviation
}

func summarizePrompt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 72 {
		s = s[:71] + "…"
	}
	return s
}

func keywords(s string) []string {
	var out []string
	for _, w := range strings.Fields(s) {
		w = strings.Trim(w, ".,;:!?\"'")
		if len(w) < 4 {
			continue
		}
		if isStopword(w) {
			continue
		}
		out = append(out, w)
		if len(out) >= 6 {
			break
		}
	}
	return out
}

func isStopword(w string) bool {
	switch w {
	case "this", "that", "with", "from", "have", "your", "please", "want", "need", "make", "help", "what", "when", "where", "which", "about", "would", "could", "should":
		return true
	}
	return false
}

// RunPlan shows the FMS-style session route.
func RunPlan(w io.Writer) {
	cwd, _ := os.Getwd()
	snap := readSnapshot(resolveSession(cwd))
	fmt.Fprintln(w, "Cockpit flight plan (FMS)")
	fmt.Fprintf(w, "  phase:      %s\n", snap.Phase)
	fmt.Fprintf(w, "  cost index: %s  (COCKPIT_COST_INDEX=eco|normal|perf)\n", snap.CostIndex)
	if snap.PlanAnchor != "" {
		fmt.Fprintf(w, "  route:      %s\n", snap.PlanAnchor)
	} else {
		fmt.Fprintln(w, "  route:      (no anchor yet — start with a clear task)")
	}
	if snap.PlanDeviation != "" {
		fmt.Fprintf(w, "  deviation:  %s\n", snap.PlanDeviation)
	}
}

// RunSystems prints an ECAM synoptic of connected systems.
func RunSystems(w io.Writer, cwd string) {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	fmt.Fprintln(w, "Cockpit systems (synoptic)")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  coding agents: %s\n", listCodingAgents(cwd))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  hooks:")
	printHookLine(w, "statusline")
	printHookLine(w, "Stop/analyze")
	printHookLine(w, "SessionEnd/cleanup")
	fmt.Fprintln(w)
	graph := hasGraphifyGraph(cwd)
	graphMark := "✗"
	if graph {
		graphMark = "✓"
	}
	fmt.Fprintf(w, "  graphify:   %s graphify-out\n", graphMark)
	mcp := listMCPServers(cwd)
	if mcp == "" {
		fmt.Fprintln(w, "  mcp:        (none)")
	} else {
		fmt.Fprintf(w, "  mcp:        %s\n", mcp)
	}
	skills := listSkills(cwd)
	if skills == "" {
		fmt.Fprintln(w, "  skills:     (none)")
	} else {
		fmt.Fprintf(w, "  skills:     %s\n", skills)
	}
	agents := listAgents(cwd)
	if agents == "" {
		fmt.Fprintln(w, "  agents:     (none)")
	} else {
		fmt.Fprintf(w, "  agents:     %s\n", agents)
	}
}

func printHookLine(w io.Writer, name string) {
	fmt.Fprintf(w, "    %-16s ✓\n", name)
}

// RunStatus prints ECAM STATUS — deferred items before next session.
func RunStatus(w io.Writer, cwd string) {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	snap := readSnapshot(resolveSession(cwd))
	st, hasState := readState()
	fmt.Fprintln(w, "Cockpit STATUS — deferred items")
	fmt.Fprintln(w)
	if !snap.GraphifyGraph && snap.Searches >= 5 {
		fmt.Fprintln(w, "  ○ graphify graph not built — consider /graphify .")
	}
	if snap.PendingSuggestions > 0 {
		fmt.Fprintf(w, "  ○ %d cockpit suggestion(s) pending — cockpit list\n", snap.PendingSuggestions)
	}
	if snap.PlanDeviation != "" {
		fmt.Fprintf(w, "  ○ plan deviation — %s\n", snap.PlanDeviation)
	}
	if snap.ToolErrors >= 8 {
		fmt.Fprintf(w, "  ○ %d tool faults (%d not-found) — cockpit checklist faults\n", snap.ToolErrors, snap.NotFoundErrors)
	}
	if hasState && st.CtxPct >= 75 {
		fmt.Fprintf(w, "  ○ context at %d%% — cockpit checklist context\n", st.CtxPct)
	}
	if mcp := listMCPServers(cwd); mcp == "" {
		fmt.Fprintln(w, "  ○ no MCP servers in project")
	}
	if snap.PlanAnchor == "" && snap.Phase == string(PhasePreflight) {
		fmt.Fprintln(w, "  ○ no clear session route — state your goal in the first prompt")
	}
	if snap.GraphifyGraph && snap.Searches >= 10 {
		fmt.Fprintln(w, "  ○ heavy grep despite graph — try graphify query")
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Instruments nominal if nothing is listed above.")
}

// historyFile is the cross-session black-box log (one JSON line per ended
// session). It is the ccusage-style baseline: trends only mean something
// against a recorded past, so debrief can say how this session compares.
func historyFile() string { return filepath.Join(cockpitDir(), "history.jsonl") }

// sessionRecord is one ended session's final instruments.
type sessionRecord struct {
	TS         string  `json:"ts"`
	Session    string  `json:"session"`
	Cwd        string  `json:"cwd"`
	CostUSD    float64 `json:"cost_usd"`
	CtxPct     int     `json:"ctx_pct"`
	Searches   int     `json:"searches"`
	ToolErrors int     `json:"tool_errors"`
	NotFound   int     `json:"not_found"`
}

// appendHistory records the ending session's final snapshot in the black-box
// log. Called from SessionEnd cleanup before per-session artifacts are removed.
func appendHistory(session string) {
	snap := readSnapshot(session)
	if snap.Session == "" {
		return // session never produced instruments
	}
	rec := sessionRecord{
		TS:         time.Now().Format(time.RFC3339),
		Session:    safeSession(session),
		Cwd:        snap.Cwd,
		CostUSD:    snap.CostUSD,
		CtxPct:     snap.ContextUsedPct,
		Searches:   snap.Searches,
		ToolErrors: snap.ToolErrors,
		NotFound:   snap.NotFoundErrors,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(historyFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// baseline7d summarizes the last 7 days of ended sessions.
func baseline7d() (n int, cost float64, avgCtx, faults int) {
	b, err := os.ReadFile(historyFile())
	if err != nil || len(b) > 1<<20 {
		if len(b) > 1<<20 {
			b = b[len(b)-(1<<20):] // bound work on a huge log; partial first line is skipped by Unmarshal
		}
		if err != nil {
			return
		}
	}
	cutoff := time.Now().AddDate(0, 0, -7)
	ctxSum := 0
	for _, ln := range strings.Split(string(b), "\n") {
		if ln == "" {
			continue
		}
		var r sessionRecord
		if json.Unmarshal([]byte(ln), &r) != nil {
			continue
		}
		if ts, err := time.Parse(time.RFC3339, r.TS); err != nil || ts.Before(cutoff) {
			continue
		}
		n++
		cost += r.CostUSD
		ctxSum += r.CtxPct
		faults += r.ToolErrors
	}
	if n > 0 {
		avgCtx = ctxSum / n
	}
	return
}

// RunDebrief prints a post-session black-box summary.
func RunDebrief(w io.Writer, session string) {
	if session == "" {
		cwd, _ := os.Getwd()
		session = resolveSession(cwd)
	}
	snap := readSnapshot(session)
	st, _ := readState()
	fmt.Fprintln(w, "Cockpit debrief")
	fmt.Fprintf(w, "  phase:       %s\n", snap.Phase)
	fmt.Fprintf(w, "  cost index:  %s\n", snap.CostIndex)
	// prefer the session's own instruments; global state is another session's.
	if snap.CtxSize > 0 || snap.ContextUsedPct > 0 {
		fmt.Fprintf(w, "  context:     %d%%  $%.2f\n", snap.ContextUsedPct, snap.CostUSD)
	} else if st.CtxSize > 0 {
		fmt.Fprintf(w, "  context:     %d%%  $%.2f\n", st.CtxPct, st.Cost)
	}
	fmt.Fprintf(w, "  searches:    %d\n", snap.Searches)
	if snap.ToolErrors > 0 {
		fmt.Fprintf(w, "  faults:      %d tool errors (%d not-found)\n", snap.ToolErrors, snap.NotFoundErrors)
	}
	fmt.Fprintf(w, "  tools:       %s\n", snap.ToolTop)
	fmt.Fprintf(w, "  suggestions: %d pending at end\n", snap.PendingSuggestions)
	if snap.PlanAnchor != "" {
		fmt.Fprintf(w, "  route:       %s\n", snap.PlanAnchor)
	}
	if session != "" {
		logPath := sessionLogFile(session)
		if _, err := os.Stat(logPath); err == nil {
			fmt.Fprintf(w, "  log:         %s\n", logPath)
		}
	}
	if n, cost, avgCtx, faults := baseline7d(); n > 0 {
		fmt.Fprintf(w, "  baseline 7d: %d sessions · $%.2f · avg ctx %d%% · %d faults\n", n, cost, avgCtx, faults)
	}
}

func buildMemoLine(snap cockpitSnapshot, st cockpitState, dir string) string {
	graph := "no"
	if snap.GraphifyGraph || hasGraphifyGraph(dir) {
		graph = "yes"
	}
	parts := []string{
		sessionPhaseDisplay(snap.Phase),
		"graphify " + graph,
		"cost " + snap.CostIndex,
	}
	if snap.PendingSuggestions > 0 {
		word := "fixes"
		if snap.PendingSuggestions == 1 {
			word = "fix"
		}
		parts = append(parts, fmt.Sprintf("%d %s", snap.PendingSuggestions, word))
	}
	if snap.Phase == string(PhaseEmergency) {
		parts = append(parts, "pressure")
	}
	if isDaemonRunning() {
		parts = append(parts, "advisor on")
	} else {
		parts = append(parts, "advisor off")
	}
	return strings.Join(parts, " · ")
}

func phaseLabel(p string) string {
	if p == "" {
		return sessionPhaseDisplay("cruise")
	}
	return sessionPhaseDisplay(p)
}

// stripSeverityPrefix returns display text for a stored suggestion line.
func stripSeverityPrefix(s string) string {
	if _, body, ok := parseSeverityPrefix(s); ok {
		return body
	}
	return s
}
