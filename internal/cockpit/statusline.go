package cockpit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	maxHintBytes = 8 * 1024
	hintMaxAge   = 24 * time.Hour
)

type slInput struct {
	Model struct {
		DisplayName string `json:"display_name"`
	} `json:"model"`
	ContextWindow struct {
		UsedPercentage    float64 `json:"used_percentage"`
		TotalInputTokens  int64   `json:"total_input_tokens"`
		ContextWindowSize int64   `json:"context_window_size"`
		TotalOutputTokens int64   `json:"total_output_tokens"`
		CurrentUsage      struct {
			CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
		} `json:"current_usage"`
	} `json:"context_window"`
	Exceeds200k bool `json:"exceeds_200k_tokens"`
	Cost        struct {
		TotalCostUSD      float64 `json:"total_cost_usd"`
		TotalLinesAdded   int64   `json:"total_lines_added"`
		TotalLinesRemoved int64   `json:"total_lines_removed"`
	} `json:"cost"`
	RateLimits struct {
		FiveHour struct {
			UsedPercentage float64 `json:"used_percentage"`
		} `json:"five_hour"`
		SevenDay struct {
			UsedPercentage float64 `json:"used_percentage"`
		} `json:"seven_day"`
	} `json:"rate_limits"`
	Effort struct {
		Level string `json:"level"`
	} `json:"effort"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
	} `json:"workspace"`
	Cwd      string `json:"cwd"`
	Worktree struct {
		Branch string `json:"branch"`
	} `json:"worktree"`
	PR struct {
		Number      json.Number `json:"number"`
		ReviewState string      `json:"review_state"`
	} `json:"pr"`
}

// RunStatusline reads the status-line JSON from r and writes the rendered bar to w.
func RunStatusline(r io.Reader, w io.Writer) {
	data, _ := io.ReadAll(r)
	var in slInput
	_ = json.Unmarshal(data, &in)
	writeState(in)
	patchSnapshotFromStatusline(in)
	chime := maybeChime(int(in.ContextWindow.UsedPercentage))
	snap := readSnapshot()
	st, _ := readState()
	hints := readSuggestions()
	classified := parseSuggestionStore(hints, snap, st)
	if os.Getenv("COCKPIT_DEBUG") != "" {
		_ = os.WriteFile(filepath.Join(ConfigDir(), ".cockpit-cols"),
			[]byte(fmt.Sprintf("COLUMNS=%q -> termCols=%d\n", os.Getenv("COLUMNS"), termCols())), 0o644)
	}
	if chime != "" {
		fmt.Fprint(w, chime)
	}
	for _, line := range renderStatusline(in, classified, snap, st) {
		fmt.Fprintln(w, line)
	}
}

func patchSnapshotFromStatusline(in slInput) {
	ctxPct := int(in.ContextWindow.UsedPercentage)
	snap := readSnapshot()
	st, _ := readState()
	snap.ContextUsedPct = ctxPct
	fiveH := int(in.RateLimits.FiveHour.UsedPercentage)
	if fiveH == 0 {
		fiveH = st.FiveH
	}
	sevenD := int(in.RateLimits.SevenDay.UsedPercentage)
	if sevenD == 0 {
		sevenD = st.SevenD
	}
	snap.Rate5hPct = fiveH
	snap.Rate7dPct = sevenD
	snap.PendingSuggestions = countApplyable(parseSuggestionStore(readSuggestions(), snap, st))
	sig := Signals{
		Turns:          20,
		ContextUsedPct: ctxPct,
		Rate5hPct:      fiveH,
		Rate7dPct:      sevenD,
		Searches:       snap.Searches,
		GraphifyGraph:  snap.GraphifyGraph,
	}
	snap.Phase = string(detectPhase(sig, in.PR.ReviewState))
	writeSnapshot(snap)
}

// writeState persists the real context window, fill %, cost, and rate-limit
// pressure that Claude Code provides here, for the analyzer (and a future
// session's statusline) to consume. Merges onto the existing state rather than
// overwriting wholesale, so a render that is momentarily missing context data
// (window size 0) or rate-limit data (payload has no rate_limits block, which
// is indistinguishable from a real 0%) still persists whichever fields it does
// have instead of clobbering good stored values. Best-effort.
func writeState(in slInput) {
	st, _ := readState()
	if in.ContextWindow.ContextWindowSize > 0 {
		st.CtxSize = in.ContextWindow.ContextWindowSize
		st.CtxPct = int(in.ContextWindow.UsedPercentage)
		st.CtxTokens = in.ContextWindow.TotalInputTokens
		st.Cost = in.Cost.TotalCostUSD
	}
	if v := int(in.RateLimits.FiveHour.UsedPercentage); v > 0 {
		st.FiveH = v
	}
	if v := int(in.RateLimits.SevenDay.UsedPercentage); v > 0 {
		st.SevenD = v
	}
	if b, err := json.Marshal(st); err == nil {
		_ = os.WriteFile(stateFile(), b, 0o644)
	}
}

// readSuggestions returns all current suggestion lines (the full session report)
// so the bar can show every lever, not just the top one. Bounded by size and
// staleness; falls back to the single hint file if no report exists.
func readSuggestions() []string {
	if lines := readLinesBounded(reportFile()); len(lines) > 0 {
		return lines
	}
	return readLinesBounded(hintFile())
}

func readLinesBounded(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && time.Since(st.ModTime()) > hintMaxAge {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(f, maxHintBytes))
	if err != nil {
		debugLog("statusline: read %s: %v", path, err)
		return nil
	}
	var out []string
	for _, ln := range strings.Split(string(b), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
		if len(out) >= 4 { // safety cap on suggestion rows
			break
		}
	}
	return out
}

// renderStatusline builds the rows. Pure (no IO) so it is unit-testable; the
// git fallback only runs when the worktree branch is absent.
func renderStatusline(in slInput, hints []classifiedSuggestion, snap cockpitSnapshot, st cockpitState) []string {
	dir := in.Workspace.CurrentDir
	if dir == "" {
		dir = in.Cwd
	}
	if dir == "" {
		dir = "."
	}

	ctxPct := int(in.ContextWindow.UsedPercentage)
	ctxWarn := ""
	if ctxPct >= 90 {
		ctxWarn = " " + red + bold + "⚠ /compact" + rst
	}
	ctxSeg := formatCtxInstrument(ctxPct, in.ContextWindow.TotalInputTokens,
		in.ContextWindow.ContextWindowSize, ctxWarn)

	branch := in.Worktree.Branch
	if branch == "" {
		branch = gitBranch(dir)
	}
	loc := cyan + filepath.Base(dir) + rst
	if branch != "" {
		loc += " " + dim + "⎇" + rst + magenta + branch + rst
	}
	if num := in.PR.Number.String(); num != "" {
		prColor := dim
		switch in.PR.ReviewState {
		case "APPROVED":
			prColor = green
		case "CHANGES_REQUESTED":
			prColor = red
		case "REVIEW_REQUIRED", "COMMENTED":
			prColor = yellow
		}
		loc += " " + prColor + "⇡#" + num + rst
	}

	phaseSeg := formatPhaseBadge(snap.Phase)

	modelName := in.Model.DisplayName
	if modelName == "" {
		modelName = "claude"
	}
	modelSeg := blue + bold + modelName + rst
	if i := strings.Index(modelName, " ("); i >= 0 {
		modelSeg = blue + bold + modelName[:i] + rst + dim + modelName[i:] + rst
	}
	if in.Effort.Level != "" {
		modelSeg += " " + dim + in.Effort.Level + rst
	}

	churnSeg := green + "+" + strconv.FormatInt(in.Cost.TotalLinesAdded, 10) + rst +
		dim + "/" + rst + red + "-" + strconv.FormatInt(in.Cost.TotalLinesRemoved, 10) + rst
	tokSeg := dim + "out " + rst + fmtTokens(in.ContextWindow.TotalOutputTokens) +
		dim + " · cache " + rst + fmtTokens(in.ContextWindow.CurrentUsage.CacheReadInputTokens)
	workSeg := churnSeg + dim + " · " + rst + tokSeg

	fiveH := int(in.RateLimits.FiveHour.UsedPercentage)
	if fiveH == 0 {
		fiveH = st.FiveH
	}
	sevenD := int(in.RateLimits.SevenDay.UsedPercentage)
	if sevenD == 0 {
		sevenD = st.SevenD
	}
	rlSeg := dim + "5h " + rst + pctColor(fiveH) + bold + strconv.Itoa(fiveH) + "%" + rst +
		dim + " · 7d " + rst + pctColor(sevenD) + bold + strconv.Itoa(sevenD) + "%" + rst

	costSeg := costColor(in.Cost.TotalCostUSD) + bold +
		fmt.Sprintf("$%.2f", in.Cost.TotalCostUSD) + rst

	notes, fixes := partitionSuggestions(hints)
	mode := displayMode()
	cols := termCols()
	var rows []string
	switch mode {
	case "minimal":
		rows = []string{assembleRow([]barSeg{
			{loc, prioPinned}, {modelSeg, 1}, {ctxSeg, prioPinned},
		}, cols)}
	default:
		// The phase badge is a mode indicator, not a status field, so it leads the
		// row with a plain gap rather than a "·" separator.
		const phaseGap = "  "
		statusCols := cols - visibleWidth(phaseSeg) - displayWidth(phaseGap)
		rows = []string{
			phaseSeg + phaseGap + assembleRow([]barSeg{
				{loc, prioPinned}, {modelSeg, 2}, {ctxSeg, prioPinned},
			}, statusCols),
			assembleRow([]barSeg{
				{workSeg, 1}, {rlSeg, 2}, {costSeg, prioPinned},
			}, cols),
			formatAdvisorHeader(isDaemonRunning(), len(fixes)),
		}
	}

	if mode == "debug" && snap.ToolTop != "" {
		rows = append(rows, dim+"debug · tools "+snap.ToolTop+rst)
	}

	for _, n := range notes {
		rows = append(rows, formatNoteLine(n, cols)...)
	}
	for i, f := range fixes {
		rows = append(rows, formatFixLine(i, f, cols)...)
		rows = append(rows, formatFixApply(i+1))
	}
	return rows
}

// termCols returns the terminal width from the COLUMNS env var Claude Code sets,
// defaulting to a safe 100 when absent.
func termCols() int {
	if c, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS"))); err == nil && c > 20 {
		return c
	}
	return 100
}

// wrapText word-wraps s to lines no wider than width display columns. Returns nil
// for empty input. A leading emoji counts as ~2 columns, so we keep a small
// margin to avoid the terminal re-wrapping or truncating.
func wrapText(s string, width int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	limit := width - 1
	if limit < 20 {
		limit = 20
	}
	var lines []string
	var line string
	lineLen := 0
	for _, word := range strings.Fields(s) {
		wl := displayWidth(word)
		switch {
		case line == "":
			line, lineLen = word, wl
		case lineLen+1+wl <= limit:
			line += " " + word
			lineLen += 1 + wl
		default:
			lines = append(lines, line)
			line, lineLen = word, wl
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// formatPhaseBadge renders the session-mode pill on the primary instrument row.
func formatPhaseBadge(phase string) string {
	label := sessionPhaseDisplay(phase)
	col := phaseColor(phase)
	return col + "● " + bold + label + rst
}

func phaseColor(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "emergency", "emer":
		return red
	case "preflight":
		return blue
	case "approach":
		return yellow
	case "landing":
		return cyan
	default:
		return green
	}
}

func formatCtxInstrument(pct int, used, total int64, warn string) string {
	col := green
	if pct >= 90 {
		col = red
	} else if pct >= 70 {
		col = yellow
	}
	return dim + "ctx " + rst + col + gauge(pct) + " " +
		bold + col + strconv.Itoa(pct) + "%" + rst +
		dim + " " + fmtTokens(used) + "/" + fmtTokens(total) + rst + warn
}

func formatNoteLine(h classifiedSuggestion, cols int) []string {
	prefix := dim + "  " + rst + alertChip(h.Level) + dim + "  " + rst
	prefixW := displayWidth("  note   ")
	limit := cols - prefixW - 1
	if limit < 24 {
		limit = 24
	}
	wrapped := wrapText(h.Text, limit)
	if len(wrapped) == 0 {
		return nil
	}
	var rows []string
	for j, ln := range wrapped {
		if j == 0 {
			rows = append(rows, prefix+dim+ln+rst)
		} else {
			rows = append(rows, dim+"     "+rst+dim+ln+rst)
		}
	}
	return rows
}

func formatFixLine(idx int, h classifiedSuggestion, cols int) []string {
	num := strconv.Itoa(idx + 1)
	prefix := dim + "  " + num + " " + rst + alertChip(h.Level) + dim + "  " + rst
	prefixW := displayWidth("  1 watch   ")
	limit := cols - prefixW - 1
	if limit < 24 {
		limit = 24
	}
	wrapped := wrapText(h.Text, limit)
	if len(wrapped) == 0 {
		return nil
	}
	col := alertColor(h.Level)
	var rows []string
	indent := dim + "     " + rst
	for j, ln := range wrapped {
		if j == 0 {
			rows = append(rows, prefix+col+ln+rst)
		} else {
			rows = append(rows, indent+col+ln+rst)
		}
	}
	return rows
}

// formatAdvisorHeader titles the suggestion section on its own line, so the
// controls below read as a named block instead of trailing off the metrics row.
// It doubles as the advisor's on/off indicator and shows how many controls are
// ready to apply.
func formatAdvisorHeader(on bool, applyable int) string {
	state := green + "on" + rst
	if !on {
		state = yellow + "off" + rst
	}
	h := magenta + bold + "▸ advisor " + rst + state
	if applyable > 0 {
		noun := "control"
		if applyable > 1 {
			noun += "s"
		}
		h += dim + " · " + rst + cyan + strconv.Itoa(applyable) + " " + noun + rst
	}
	return h
}

func formatFixApply(n int) string {
	return dim + "     apply · " + rst + cyan + bold + "cockpit apply " + strconv.Itoa(n) + rst
}

func gitBranch(dir string) string {
	out, err := exec.Command("git", "-C", dir, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
