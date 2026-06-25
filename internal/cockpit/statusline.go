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
	writeState(in) // bridge the authoritative context/cost/rate data to the analyzer
	if os.Getenv("COCKPIT_DEBUG") != "" {
		_ = os.WriteFile(filepath.Join(ConfigDir(), ".cockpit-cols"),
			[]byte(fmt.Sprintf("COLUMNS=%q -> termCols=%d\n", os.Getenv("COLUMNS"), termCols())), 0o644)
	}
	for _, line := range renderStatusline(in, readSuggestions()) {
		fmt.Fprintln(w, line)
	}
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
// git fallback only runs when the worktree branch is absent. Each suggestion in
// hints becomes one or more wrapped rows below the two instrument rows.
func renderStatusline(in slInput, hints []string) []string {
	dir := in.Workspace.CurrentDir
	if dir == "" {
		dir = in.Cwd
	}
	if dir == "" {
		dir = "."
	}

	// Gauge color/warning is driven purely by how full the window actually is.
	// (exceeds_200k_tokens is NOT used here: on a 1M window it goes true at ~20%,
	// which would fire the /compact cue 70 points too early.)
	ctxPct := int(in.ContextWindow.UsedPercentage)
	ctxColor, warn := green, ""
	if ctxPct >= 90 {
		// U+FE0F forces emoji (not text) presentation of the warning sign.
		ctxColor, warn = red, " "+red+bold+"⚠️ /compact"+rst
	} else if ctxPct >= 70 {
		ctxColor = yellow
	}
	// dim "ctx" label, colored gauge, bold colored %, dim token detail.
	ctxSeg := dim + "ctx " + rst + ctxColor + gauge(ctxPct) + " " +
		bold + ctxColor + strconv.Itoa(ctxPct) + "%" + rst +
		" " + dim + fmtTokens(in.ContextWindow.TotalInputTokens) + "/" +
		fmtTokens(in.ContextWindow.ContextWindowSize) + rst + warn

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

	// Model name pops (bold blue); the "(1M context)" parenthetical stays dim.
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

	// Row 2: bright values, dim labels — readable, not all-gray.
	churnSeg := green + "+" + strconv.FormatInt(in.Cost.TotalLinesAdded, 10) + rst +
		dim + "/" + rst + red + "-" + strconv.FormatInt(in.Cost.TotalLinesRemoved, 10) + rst
	tokSeg := dim + "out " + rst + fmtTokens(in.ContextWindow.TotalOutputTokens) +
		dim + " · cache " + rst + fmtTokens(in.ContextWindow.CurrentUsage.CacheReadInputTokens)
	workSeg := churnSeg + dim + " · " + rst + tokSeg

	// Each rate-limit window is colored independently so a hot 5h doesn't paint 7d red.
	fiveH := int(in.RateLimits.FiveHour.UsedPercentage)
	sevenD := int(in.RateLimits.SevenDay.UsedPercentage)
	rlSeg := dim + "5h " + rst + pctColor(fiveH) + bold + strconv.Itoa(fiveH) + "%" + rst +
		dim + " · 7d " + rst + pctColor(sevenD) + bold + strconv.Itoa(sevenD) + "%" + rst

	// Cost colored by magnitude so a high bill is obvious at a glance.
	costSeg := costColor(in.Cost.TotalCostUSD) + bold +
		fmt.Sprintf("$%.2f", in.Cost.TotalCostUSD) + rst

	sep := dim + " │ " + rst
	rows := []string{
		loc + sep + modelSeg + sep + ctxSeg,
		workSeg + sep + rlSeg + sep + costSeg,
	}
	// Each suggestion wraps across as many rows as needed so full sentences show
	// instead of being truncated with "…".
	cols := termCols()
	for _, h := range hints {
		for _, ln := range wrapText(h, cols) {
			rows = append(rows, yellow+ln+rst)
		}
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

func gitBranch(dir string) string {
	out, err := exec.Command("git", "-C", dir, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
