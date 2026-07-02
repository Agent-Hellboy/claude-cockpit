package cockpit

import (
	"fmt"
	"io"
	"os"
	"strings"
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
	if st.CtxSize > 0 {
		fmt.Fprintf(w, "  context:     %d%%  $%.2f\n", st.CtxPct, st.Cost)
	}
	fmt.Fprintf(w, "  searches:    %d\n", snap.Searches)
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
