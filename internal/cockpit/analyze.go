package cockpit

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// transcript entry (subset we care about).
type tEntry struct {
	Message struct {
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   struct {
			InputTokens          int64 `json:"input_tokens"`
			CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type contentItem struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Name  string `json:"name"`
	Input struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
	} `json:"input"`
}

type stopInput struct {
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	SessionID      string `json:"session_id"`
}

var searchRe = regexp.MustCompile(`\b(grep|rg|find)\b`)

// RunAnalyze implements the Stop hook: gather cheap signals, throttle by an
// auto-scaling cadence, and hand off to a detached worker that asks a cheap
// model for token-saving suggestions. Returns fast so the turn never waits.
func RunAnalyze(r io.Reader) {
	if os.Getenv("MODEL_HINT_GUARD") != "" {
		return // don't run inside the background `claude -p`
	}
	data, _ := io.ReadAll(r)
	var in stopInput
	if json.Unmarshal(data, &in) != nil || in.TranscriptPath == "" {
		return
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return
	}

	n := bumpCounter(in.SessionID)
	k := 10
	switch {
	case n >= 25:
		k = 2
	case n >= 10:
		k = 5
	}
	if n%k != 0 {
		return
	}

	signals := gatherSignals(in, n)
	spawnWorker(signals)
}

func gatherSignals(in stopInput, turns int) string {
	entries := tailEntries(in.TranscriptPath, 3000)

	hist := map[string]int{}
	reads := map[string]int{}
	greps := 0
	var lastModel string
	var ctxTokens int64
	var prompts []string

	for _, e := range entries {
		role := e.Message.Role
		if role == "assistant" {
			if e.Message.Model != "" {
				lastModel = e.Message.Model
			}
			if t := e.Message.Usage.InputTokens + e.Message.Usage.CacheReadInputTokens; t > 0 {
				ctxTokens = t
			}
		}
		// content may be a string (user) or an array (tool uses / text blocks).
		var items []contentItem
		if json.Unmarshal(e.Message.Content, &items) == nil {
			for _, it := range items {
				if it.Type == "tool_use" {
					hist[it.Name]++
					switch it.Name {
					case "Grep":
						greps++
					case "Bash":
						if searchRe.MatchString(it.Input.Command) {
							greps++
						}
					case "Read":
						if it.Input.FilePath != "" {
							reads[it.Input.FilePath]++
						}
					}
				}
				if role == "user" && it.Type == "text" && it.Text != "" {
					prompts = append(prompts, it.Text)
				}
			}
		} else if role == "user" {
			var s string
			if json.Unmarshal(e.Message.Content, &s) == nil && s != "" {
				prompts = append(prompts, s)
			}
		}
	}

	dups := 0
	for _, c := range reads {
		if c >= 3 {
			dups++
		}
	}

	graph := "no"
	files, est := "?", "n/a"
	if fileExists(filepath.Join(in.Cwd, "graphify-out", "graph.json")) {
		graph = "yes"
	} else {
		nf := countSourceFiles(in.Cwd)
		files = strconv.Itoa(nf)
		est = graphETA(nf)
	}

	if len(prompts) > 8 {
		prompts = prompts[len(prompts)-8:]
	}

	return fmt.Sprintf(`turns=%d  model=%s  approx_context_tokens=%d
tool_histogram: %s
searches=%d  files_reread_3x+=%d
graphify_graph=%s  repo_source_files=%s  est_graph_build=%s
available_skills: %s
recent_prompts: %s`,
		turns, fallback(lastModel, "?"), ctxTokens,
		histString(hist), greps, dups,
		graph, files, est,
		listSkills(in.Cwd), strings.Join(prompts, " "))
}

func tailEntries(path string, max int) []tEntry {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	out := make([]tEntry, 0, len(lines))
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		var e tEntry
		if json.Unmarshal([]byte(ln), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

func histString(h map[string]int) string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, h[k]))
	}
	return strings.Join(parts, " ")
}

var srcExts = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".py": true, ".rs": true, ".java": true, ".rb": true, ".c": true,
	".cc": true, ".cpp": true, ".h": true, ".hpp": true, ".cs": true,
	".kt": true, ".swift": true,
}

var errStopWalk = fmt.Errorf("stop")

func countSourceFiles(root string) int {
	n := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "graphify-out", "dist", "build":
				return fs.SkipDir
			}
			return nil
		}
		if srcExts[strings.ToLower(filepath.Ext(p))] {
			n++
			if n >= 30000 {
				return errStopWalk
			}
		}
		return nil
	})
	return n
}

func graphETA(files int) string {
	switch {
	case files < 300:
		return "at least ~1-2 min"
	case files < 1000:
		return "at least ~2-4 min"
	case files < 3000:
		return "at least ~4-8 min"
	case files < 6000:
		return "at least ~8-15 min"
	default:
		return "15+ min"
	}
}

func listSkills(cwd string) string {
	set := map[string]bool{}
	for _, d := range []string{
		filepath.Join(cwd, ".codex", "skills"),
		filepath.Join(cwd, ".claude", "skills"),
		filepath.Join(ConfigDir(), "skills"),
	} {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				set[e.Name()] = true
			}
		}
	}
	names := make([]string, 0, len(set))
	for k := range set {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}

func bumpCounter(sid string) int {
	if sid == "" {
		sid = "x"
	}
	p := filepath.Join(ConfigDir(), ".sa-count-"+sid)
	n := 0
	if b, err := os.ReadFile(p); err == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	n++
	_ = os.WriteFile(p, []byte(strconv.Itoa(n)), 0o644)
	return n
}

// spawnWorker writes the signals to a temp file and starts `cockpit worker` as a
// fully detached process (own process group, /dev/null fds) so it outlives this
// hook and never blocks the turn.
func spawnWorker(signals string) {
	tmp, err := os.CreateTemp("", "cockpit-sig-*")
	if err != nil {
		return
	}
	_, _ = tmp.WriteString(signals)
	_ = tmp.Close()

	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "worker", tmp.Name())
	cmd.Env = append(os.Environ(), "MODEL_HINT_GUARD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	}
	_ = cmd.Start() // do not Wait — let it run detached
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func fallback(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
