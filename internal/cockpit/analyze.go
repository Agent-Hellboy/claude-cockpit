package cockpit

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
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
var secretRe = regexp.MustCompile(`(?i)(api[_-]?key|authorization|bearer|password|token|secret)\s*[:=]\s*['"]?[^'"\s]+`)

// cockpitState mirrors the snapshot the status line writes from Claude Code's
// authoritative status payload (see statusline.go writeState).
type cockpitState struct {
	CtxSize   int64   `json:"ctx_size"`
	CtxPct    int     `json:"ctx_pct"`
	CtxTokens int64   `json:"ctx_tokens"`
	Cost      float64 `json:"cost"`
	FiveH     int     `json:"five_h"`
	SevenD    int     `json:"seven_d"`
}

func readState() (cockpitState, bool) {
	b, err := os.ReadFile(stateFile())
	if err != nil {
		return cockpitState{}, false
	}
	var s cockpitState
	if json.Unmarshal(b, &s) != nil {
		return cockpitState{}, false
	}
	return s, true
}

type Signals struct {
	Turns               int
	Model               string
	ApproxContextTokens int64
	ContextWindow       int64
	ContextUsedPct      int
	ContextSource       string
	CostUSD             float64
	Rate5hPct           int
	Rate7dPct           int
	ToolHistogram       map[string]int
	Searches            int
	FilesReread3x       int
	GraphifyGraph       bool
	RepoSourceFiles     string
	EstGraphBuild       string
	AvailableSkills     string
	AvailableAgents     string
	AvailableMCPServers string
	AvailablePlugins    string
	RecentPrompts       []string
}

// RunAnalyze implements the Stop hook: gather cheap cockpit signals, throttle by
// a session-length cadence, and hand off to a detached worker that asks a cheap
// model for control suggestions. Returns fast so the turn never waits.
func RunAnalyze(r io.Reader) {
	if os.Getenv("MODEL_HINT_GUARD") != "" {
		return // don't run inside the background `claude -p`
	}
	if os.Getenv("COCKPIT_ANALYZE_DISABLE") == "1" {
		return
	}
	data, _ := io.ReadAll(r)
	var in stopInput
	if json.Unmarshal(data, &in) != nil || in.TranscriptPath == "" {
		debugLog("analyze: invalid stop hook input")
		return
	}
	if _, err := exec.LookPath("claude"); err != nil {
		debugLog("analyze: claude not found: %v", err)
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
		logf(in.SessionID, "analyze: turn %d (cadence k=%d) — skip", n, k)
		return
	}
	logf(in.SessionID, "analyze: turn %d (cadence k=%d) — run", n, k)

	signals := formatSignals(collectSignals(in, n))
	spawnWorker(signals, in.SessionID)
}

func gatherSignals(in stopInput, turns int) string {
	return formatSignals(collectSignals(in, turns))
}

func collectSignals(in stopInput, turns int) Signals {
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

	files, est := "?", "n/a"
	graph := hasGraphifyGraph(in.Cwd)
	if !graph {
		nf := countSourceFiles(in.Cwd)
		files = strconv.Itoa(nf)
		est = graphETA(nf)
	}

	if len(prompts) > 8 {
		prompts = prompts[len(prompts)-8:]
	}
	if os.Getenv("COCKPIT_ANALYZE_PROMPTS") == "0" {
		prompts = nil
	}
	for i := range prompts {
		prompts[i] = redactSecrets(prompts[i])
	}

	// Prefer the authoritative context/cost/rate snapshot the status line captured
	// from Claude Code. Fall back to inferring the window from the model name only
	// when no snapshot exists.
	window := inferContextWindow(lastModel)
	usedPct := 0
	if window > 0 {
		usedPct = int(ctxTokens * 100 / window)
	}
	ctxSource := "inferred"
	var costUSD float64
	var rate5h, rate7d int
	if st, ok := readState(); ok && st.CtxSize > 0 {
		window = st.CtxSize
		usedPct = st.CtxPct
		if st.CtxTokens > 0 {
			ctxTokens = st.CtxTokens
		}
		costUSD, rate5h, rate7d = st.Cost, st.FiveH, st.SevenD
		ctxSource = "actual"
	}

	return Signals{
		Turns:               turns,
		Model:               fallback(lastModel, "?"),
		ApproxContextTokens: ctxTokens,
		ContextWindow:       window,
		ContextUsedPct:      usedPct,
		ContextSource:       ctxSource,
		CostUSD:             costUSD,
		Rate5hPct:           rate5h,
		Rate7dPct:           rate7d,
		ToolHistogram:       hist,
		Searches:            greps,
		FilesReread3x:       dups,
		GraphifyGraph:       graph,
		RepoSourceFiles:     files,
		EstGraphBuild:       est,
		AvailableSkills:     listSkills(in.Cwd),
		AvailableAgents:     listAgents(in.Cwd),
		AvailableMCPServers: listMCPServers(in.Cwd),
		AvailablePlugins:    listPlugins(in.Cwd),
		RecentPrompts:       prompts,
	}
}

func formatSignals(s Signals) string {
	graph := "no"
	if s.GraphifyGraph {
		graph = "yes"
	}
	return fmt.Sprintf(`turns=%d  model=%s  approx_context_tokens=%d  context_window=%d  context_used_pct=%d (%s)
cost_usd=%.2f  rate_5h_pct=%d  rate_7d_pct=%d
tool_histogram: %s
searches=%d  files_reread_3x+=%d
graphify_graph=%s  repo_source_files=%s  est_graph_build=%s
available_skills: %s
available_agents: %s
available_mcp_servers: %s
available_plugins: %s
recent_prompts: %s`,
		s.Turns, fallback(s.Model, "?"), s.ApproxContextTokens, s.ContextWindow, s.ContextUsedPct, fallback(s.ContextSource, "inferred"),
		s.CostUSD, s.Rate5hPct, s.Rate7dPct,
		histString(s.ToolHistogram), s.Searches, s.FilesReread3x,
		graph, s.RepoSourceFiles, s.EstGraphBuild,
		s.AvailableSkills,
		s.AvailableAgents,
		s.AvailableMCPServers,
		s.AvailablePlugins,
		strings.Join(s.RecentPrompts, " "))
}

// inferContextWindow guesses the model's context window from its name: the
// 1M-context Opus variant vs the standard 200k window. Used to compute fill %.
func inferContextWindow(model string) int64 {
	m := strings.ToLower(model)
	if strings.Contains(m, "1m") || strings.Contains(m, "[1m]") {
		return 1_000_000
	}
	return 200_000
}

func tailEntries(path string, max int) []tEntry {
	if max <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	lines := make([]string, max)
	count := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		lines[count%max] = sc.Text()
		count++
	}
	if err := sc.Err(); err != nil {
		debugLog("tailEntries: scan %s: %v", path, err)
		return nil
	}
	n := count
	if n > max {
		n = max
	}
	start := 0
	if count > max {
		start = count % max
	}
	out := make([]tEntry, 0, n)
	for i := 0; i < n; i++ {
		ln := lines[(start+i)%max]
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

func redactSecrets(s string) string {
	return secretRe.ReplaceAllString(s, "$1=[redacted]")
}

func hasGraphifyGraph(cwd string) bool {
	for _, marker := range []string{
		filepath.Join("graphify-out", "graph.json"),
		filepath.Join("graphify-out", "entities.jsonl"),
		filepath.Join("graphify-out", "relationships.jsonl"),
		filepath.Join(".graphify", "graph.json"),
	} {
		if fileExists(filepath.Join(cwd, marker)) {
			return true
		}
	}
	return false
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
	return listDirs([]string{
		filepath.Join(cwd, ".codex", "skills"),
		filepath.Join(cwd, ".claude", "skills"),
		filepath.Join(ConfigDir(), "skills"),
	})
}

func listAgents(cwd string) string {
	return listDirs([]string{
		filepath.Join(cwd, ".claude", "agents"),
		filepath.Join(ConfigDir(), "agents"),
	})
}

func listDirs(dirs []string) string {
	set := map[string]bool{}
	for _, d := range dirs {
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

func listMCPServers(cwd string) string {
	type mcpConfig struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	set := map[string]bool{}
	for _, p := range []string{
		filepath.Join(cwd, ".mcp.json"),
		filepath.Join(ConfigDir(), "settings.json"),
		homeClaudeJSON(),
	} {
		if p == "" {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil || len(b) > 512*1024 {
			continue
		}
		var cfg mcpConfig
		if json.Unmarshal(b, &cfg) != nil {
			continue
		}
		for name := range cfg.MCPServers {
			set[name] = true
		}
	}
	return sortedSet(set)
}

func listPlugins(cwd string) string {
	set := map[string]bool{}
	for _, root := range []string{
		filepath.Join(cwd, ".claude", "skills"),
		filepath.Join(ConfigDir(), "skills"),
		filepath.Join(cwd, ".claude", "plugins"),
		filepath.Join(ConfigDir(), "plugins"),
	} {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if fileExists(filepath.Join(root, e.Name(), ".claude-plugin", "plugin.json")) {
				set[e.Name()] = true
			}
		}
	}
	return sortedSet(set)
}

func homeClaudeJSON() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}

func sortedSet(set map[string]bool) string {
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
	p := filepath.Join(ConfigDir(), ".sa-count-"+sessionKey(sid))
	n := 0
	if b, err := os.ReadFile(p); err == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	n++
	_ = os.WriteFile(p, []byte(strconv.Itoa(n)), 0o644)
	return n
}

func sessionKey(sid string) string {
	sum := sha256.Sum256([]byte(sid))
	return hex.EncodeToString(sum[:])[:16]
}

// spawnWorker writes the signals to the session's signals file (kept for the
// session, not a throwaway temp) and starts `cockpit worker` as a fully detached
// process (own process group, /dev/null fds) so it outlives this hook and never
// blocks the turn.
func spawnWorker(signals, session string) {
	if err := os.MkdirAll(logDir(), 0o755); err != nil {
		logf(session, "spawnWorker: mkdir logs: %v", err)
		return
	}
	sigPath := sessionSignalsFile(session)
	if err := os.WriteFile(sigPath, []byte(signals), 0o644); err != nil {
		logf(session, "spawnWorker: write signals: %v", err)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		logf(session, "spawnWorker: executable: %v", err)
		return
	}
	cmd := exec.Command(exe, "worker", sigPath, session)
	cmd.Env = append(os.Environ(), "MODEL_HINT_GUARD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	}
	if err := cmd.Start(); err != nil {
		logf(session, "spawnWorker: start: %v", err)
	}
}

// RunCleanup removes a session's transient artifacts. Invoked by the SessionEnd
// hook so nothing is deleted mid-session — signals and logs persist until the
// session actually ends.
func RunCleanup(r io.Reader) {
	data, _ := io.ReadAll(r)
	var in stopInput
	_ = json.Unmarshal(data, &in)
	if in.SessionID == "" {
		return
	}
	logf(in.SessionID, "cleanup: session ended — removing transient artifacts")
	for _, p := range []string{
		sessionSignalsFile(in.SessionID),
		filepath.Join(ConfigDir(), ".sa-count-"+sessionKey(in.SessionID)),
		hintFile(), reportFile(), stateFile(),
	} {
		_ = os.Remove(p)
	}
	// Keep the .log itself as the durable record; the user can prune cockpit-logs.
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
