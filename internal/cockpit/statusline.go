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
	for _, line := range renderStatusline(in, readHint()) {
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

func readHint() string {
	f, err := os.Open(hintFile())
	if err != nil {
		return ""
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && time.Since(st.ModTime()) > hintMaxAge {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(f, maxHintBytes))
	if err != nil {
		debugLog("statusline: read hint: %v", err)
		return ""
	}
	return strings.TrimSpace(string(b))
}

// renderStatusline builds the rows. Pure (no IO) so it is unit-testable; the
// git fallback only runs when the worktree branch is absent.
func renderStatusline(in slInput, hint string) []string {
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
	if hint != "" {
		rows = append(rows, yellow+hint+rst)
	}
	return rows
}

func gitBranch(dir string) string {
	out, err := exec.Command("git", "-C", dir, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
