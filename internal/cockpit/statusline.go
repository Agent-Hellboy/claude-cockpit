package cockpit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	for _, line := range renderStatusline(in, readHint()) {
		fmt.Fprintln(w, line)
	}
}

func readHint() string {
	b, err := os.ReadFile(hintFile())
	if err != nil {
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

	ctxPct := int(in.ContextWindow.UsedPercentage)
	ctxColor, warn := green, ""
	if in.Exceeds200k || ctxPct >= 90 {
		ctxColor, warn = red, " "+red+bold+"⚠ /compact"+rst
	} else if ctxPct >= 70 {
		ctxColor = yellow
	}
	ctxSeg := fmt.Sprintf("%sctx %s%s %d%%%s%s %s/%s%s%s",
		ctxColor, bold, gauge(ctxPct), ctxPct, rst, ctxColor,
		fmtTokens(in.ContextWindow.TotalInputTokens),
		fmtTokens(in.ContextWindow.ContextWindowSize), rst, warn)

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

	modelName := in.Model.DisplayName
	if modelName == "" {
		modelName = "claude"
	}
	modelSeg := blue + modelName + rst
	if in.Effort.Level != "" {
		modelSeg += " " + dim + in.Effort.Level + rst
	}

	workSeg := fmt.Sprintf("%s%s+%d%s%s/%s-%d%s%s · out %s · cache %s%s",
		dim, green, in.Cost.TotalLinesAdded, rst, dim, red, in.Cost.TotalLinesRemoved, rst,
		dim, fmtTokens(in.ContextWindow.TotalOutputTokens),
		fmtTokens(in.ContextWindow.CurrentUsage.CacheReadInputTokens), rst)

	fiveH := int(in.RateLimits.FiveHour.UsedPercentage)
	sevenD := int(in.RateLimits.SevenDay.UsedPercentage)
	hi := fiveH
	if sevenD > hi {
		hi = sevenD
	}
	rlColor := green
	if hi >= 90 {
		rlColor = red
	} else if hi >= 75 {
		rlColor = yellow
	}
	rlSeg := fmt.Sprintf("%s5h %d%% · 7d %d%%%s", rlColor, fiveH, sevenD, rst)
	costSeg := fmt.Sprintf("%s$%.2f%s", dim, in.Cost.TotalCostUSD, rst)

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
