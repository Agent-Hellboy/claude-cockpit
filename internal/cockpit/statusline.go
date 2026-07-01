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
	snap.ContextUsedPct = ctxPct
	snap.Rate5hPct = int(in.RateLimits.FiveHour.UsedPercentage)
	snap.PendingSuggestions = len(readSuggestions())
	sig := Signals{
		Turns:          20,
		ContextUsedPct: ctxPct,
		Rate5hPct:      snap.Rate5hPct,
		Rate7dPct:      int(in.RateLimits.SevenDay.UsedPercentage),
		Searches:       snap.Searches,
		GraphifyGraph:  snap.GraphifyGraph,
	}
	snap.Phase = string(detectPhase(sig, in.PR.ReviewState))
	writeSnapshot(snap)
}

// writeState persists the real context window, fill %, cost, and rate-limit
// pressure that Claude Code provides here, for the analyzer to consume. Only
// written when the window size is known, so a render without context data does
// not clobber a good snapshot. Best-effort.
func writeState(in slInput) {
	if in.ContextWindow.ContextWindowSize <= 0 {
		return
	}
	st := cockpitState{
		CtxSize:   in.ContextWindow.ContextWindowSize,
		CtxPct:    int(in.ContextWindow.UsedPercentage),
		CtxTokens: in.ContextWindow.TotalInputTokens,
		Cost:      in.Cost.TotalCostUSD,
		FiveH:     int(in.RateLimits.FiveHour.UsedPercentage),
		SevenD:    int(in.RateLimits.SevenDay.UsedPercentage),
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

	phase := phaseLabel(snap.Phase)
	if phase == "" {
		phase = "cruise"
	}
	phaseSeg := formatPhaseBadge(phase)

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
	sevenD := int(in.RateLimits.SevenDay.UsedPercentage)
	rlSeg := dim + "5h " + rst + pctColor(fiveH) + bold + strconv.Itoa(fiveH) + "%" + rst +
		dim + " · 7d " + rst + pctColor(sevenD) + bold + strconv.Itoa(sevenD) + "%" + rst

	costSeg := costColor(in.Cost.TotalCostUSD) + bold +
		fmt.Sprintf("$%.2f", in.Cost.TotalCostUSD) + rst

	sep := dim + " ┊ " + rst
	mode := displayMode()
	var rows []string
	switch mode {
	case "minimal":
		rows = []string{loc + sep + modelSeg + sep + ctxSeg}
	default:
		rows = []string{
			phaseSeg + loc + sep + modelSeg + sep + ctxSeg,
			dim + "SYS " + rst + workSeg + sep + rlSeg + sep + costSeg,
		}
	}

	if mode != "minimal" {
		memo := buildMemoLine(snap, st, dir)
		memo = strings.TrimPrefix(memo, "memo: ")
		rows = append(rows, dim+"▸ MEMO "+rst+dim+memo+rst)
	}
	if mode == "debug" && snap.ToolTop != "" {
		rows = append(rows, dim+"▸ DBG  tools "+snap.ToolTop+rst)
	}

	cols := termCols()
	if len(hints) > 0 && mode != "minimal" {
		rows = append(rows, ecamTopRule(cols))
	}
	for i, h := range hints {
		rows = append(rows, formatECAMMessage(i, h, cols)...)
	}
	if len(hints) > 0 {
		rows = append(rows, formatApplyCTA(len(hints)))
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

// formatPhaseBadge renders the flight-phase indicator (PFD header).
func formatPhaseBadge(phase string) string {
	label := strings.ToUpper(phaseLabel(phase))
	col := phaseColor(phase)
	return col + bold + "▌" + label + "▐" + rst + " "
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
	return dim + "CTX " + rst + col + "▕" + gauge(pct) + "▏ " +
		bold + col + strconv.Itoa(pct) + "%" + rst +
		dim + " " + fmtTokens(used) + "/" + fmtTokens(total) + rst + warn
}

func ecamTopRule(cols int) string {
	title := "ECAM"
	dashes := cols - len(title) - 5 // "╭─ " + " ╮"
	if dashes < 4 {
		dashes = 4
	}
	return dim + "╭─ " + rst + cyan + bold + title + rst + dim + strings.Repeat("─", dashes) + "╮" + rst
}

func formatECAMMessage(idx int, h classifiedSuggestion, cols int) []string {
	num := fmt.Sprintf("[%d]", idx+1)
	lvl := h.Level.String()
	prefix := dim + num + rst + " " + alertColor(h.Level) + bold + lvl + rst + dim + " │ " + rst
	prefixW := displayWidth(num + " " + lvl + " │ ")
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
	for j, ln := range wrapped {
		if j == 0 {
			rows = append(rows, prefix+col+ln+rst)
		} else {
			rows = append(rows, dim+strings.Repeat(" ", len(num)+1)+rst+dim+"│ "+rst+col+ln+rst)
		}
	}
	return rows
}

func formatApplyCTA(count int) string {
	var nums []string
	for i := 1; i <= count && i <= 9; i++ {
		nums = append(nums, cyan+bold+strconv.Itoa(i)+rst)
	}
	joined := strings.Join(nums, dim+" · "+rst)
	return dim + "╰─ " + rst + green + bold + "EXEC" + rst + dim + " ▸ " + rst +
		bold + "cockpit apply " + rst + joined +
		dim + "  ·  checklist <topic>" + rst
}

func gitBranch(dir string) string {
	out, err := exec.Command("git", "-C", dir, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
