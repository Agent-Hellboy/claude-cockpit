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
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
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
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`          // tool_use id
	ToolUseID string          `json:"tool_use_id"` // tool_result -> originating tool_use
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"` // tool_result body: string or text blocks
	Input     struct {
		Command      string `json:"command"`
		FilePath     string `json:"file_path"`
		SubagentType string `json:"subagent_type"`
		Description  string `json:"description"`
	} `json:"input"`
}

type stopInput struct {
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	SessionID      string `json:"session_id"`
}

var searchRe = regexp.MustCompile(`\b(grep|rg|find)\b`)
var secretRe = regexp.MustCompile(`(?i)(api[_-]?key|authorization|bearer|password|token|secret)\s*[:=]\s*['"]?[^'"\s]+`)

// errNotFoundRe classifies the ecosystem's most common fault family: the model
// referencing files, symbols, or commands that do not exist (sniffly measured
// this at 20-30% of all Claude Code tool errors).
var errNotFoundRe = regexp.MustCompile(`(?i)(no such file|not found|does not exist|enoent|file has not been read|string to replace)`)

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
	ToolErrors          int
	NotFoundErrors      int
	ErrorTop            string
	RepoLang            string
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

	n := bumpCounter(in.SessionID)
	k := analyzeCadence(n)
	if n > 3 && n%k != 0 {
		logf(in.SessionID, "analyze: turn %d (cadence k=%d) — skip", n, k)
		return
	}
	logf(in.SessionID, "analyze: turn %d (cadence k=%d) — run", n, k)

	sig := collectSignals(in, n)
	writeSnapshot(in.SessionID, buildSnapshot(sig, "", in.SessionID, in.Cwd))
	spawnWorker(formatSignals(sig), in.SessionID, in.Cwd)
}

// analyzeCadence returns how many turns should elapse between advisor runs once
// the early-fire window (n <= 3) has passed. COCKPIT_ANALYZE_CADENCE overrides
// the default schedule outright; otherwise cadence tightens as the session goes on.
func analyzeCadence(n int) int {
	if v := os.Getenv("COCKPIT_ANALYZE_CADENCE"); v != "" {
		if k, err := strconv.Atoi(v); err == nil && k > 0 {
			return k
		}
	}
	switch {
	case n >= 25:
		return 2
	case n >= 10:
		return 5
	default:
		return 10
	}
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
	idToTool := map[string]string{} // tool_use id -> tool name, to attribute errors
	toolErrs := map[string]int{}
	notFound := 0

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
					if it.ID != "" {
						idToTool[it.ID] = it.Name
					}
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
				// Fault acquisition: failed tool calls, classified by originating
				// tool and by the not-found family (bad paths/symbols/commands).
				if it.Type == "tool_result" && it.IsError {
					name := idToTool[it.ToolUseID]
					if name == "" {
						name = "tool"
					}
					toolErrs[name]++
					if errNotFoundRe.MatchString(toolResultText(it.Content)) {
						notFound++
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
	totalErrs := 0
	for _, n := range toolErrs {
		totalErrs += n
	}

	dups := 0
	for _, c := range reads {
		if c >= 3 {
			dups++
		}
	}

	files, est := "?", "n/a"
	graph := hasGraphifyGraph(in.Cwd)
	nf, repoLang := scanSource(in.Cwd)
	if !graph {
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

	// Prefer THIS session's authoritative context/cost captured by its own
	// statusline renders (per-session snapshot). The global state file only
	// contributes account-wide rate limits plus a last-resort context fallback —
	// it is written by whichever session rendered last, so trusting its context
	// here would classify against another session's pressure.
	window := inferContextWindow(lastModel)
	usedPct := 0
	if window > 0 {
		usedPct = int(ctxTokens * 100 / window)
	}
	ctxSource := "inferred"
	var costUSD float64
	var rate5h, rate7d int
	st, hasState := readState()
	if hasState {
		rate5h, rate7d = st.FiveH, st.SevenD
	}
	if snap := readSnapshot(in.SessionID); snap.CtxSize > 0 {
		window = snap.CtxSize
		usedPct = snap.ContextUsedPct
		if snap.CtxTokens > 0 {
			ctxTokens = snap.CtxTokens
		}
		costUSD = snap.CostUSD
		ctxSource = "actual"
	} else if hasState && st.CtxSize > 0 {
		window = st.CtxSize
		usedPct = st.CtxPct
		if st.CtxTokens > 0 {
			ctxTokens = st.CtxTokens
		}
		costUSD = st.Cost
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
		ToolErrors:          totalErrs,
		NotFoundErrors:      notFound,
		ErrorTop:            topTools(toolErrs, 2),
		RepoLang:            fallback(repoLang, "?"),
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
session_phase=%s  cost_index=%s
tool_histogram: %s
searches=%d  files_reread_3x+=%d
tool_errors=%d  not_found_errors=%d  error_top=%s
repo_lang=%s  graphify_graph=%s  repo_source_files=%s  est_graph_build=%s
available_skills: %s
available_agents: %s
available_mcp_servers: %s
available_plugins: %s
recent_prompts: %s`,
		s.Turns, fallback(s.Model, "?"), s.ApproxContextTokens, s.ContextWindow, s.ContextUsedPct, fallback(s.ContextSource, "inferred"),
		s.CostUSD, s.Rate5hPct, s.Rate7dPct,
		detectPhase(s, "").label(), costIndex(),
		histString(s.ToolHistogram), s.Searches, s.FilesReread3x,
		s.ToolErrors, s.NotFoundErrors, s.ErrorTop,
		s.RepoLang, graph, s.RepoSourceFiles, s.EstGraphBuild,
		s.AvailableSkills,
		s.AvailableAgents,
		s.AvailableMCPServers,
		s.AvailablePlugins,
		strings.Join(s.RecentPrompts, " "))
}

// toolResultText flattens a tool_result body (a plain string or a list of text
// blocks) into matchable text.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var items []contentItem
	if json.Unmarshal(raw, &items) == nil {
		var b strings.Builder
		for _, it := range items {
			if it.Type == "text" {
				b.WriteString(it.Text)
				b.WriteByte('\n')
			}
		}
		return b.String()
	}
	return ""
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

// extLang names the stack for the tool scout, so a capability search can be
// narrowed to integrations that fit the project.
var extLang = map[string]string{
	".go": "Go", ".ts": "TypeScript", ".tsx": "TypeScript/React", ".js": "JavaScript",
	".jsx": "JavaScript/React", ".py": "Python", ".rs": "Rust", ".java": "Java",
	".rb": "Ruby", ".c": "C", ".cc": "C++", ".cpp": "C++", ".h": "C/C++",
	".hpp": "C++", ".cs": "C#", ".kt": "Kotlin", ".swift": "Swift",
}

var errStopWalk = fmt.Errorf("stop")

func countSourceFiles(root string) int {
	n, _ := scanSource(root)
	return n
}

// scanSource counts source files and reports the dominant language.
func scanSource(root string) (int, string) {
	n := 0
	byExt := map[string]int{}
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
		ext := strings.ToLower(filepath.Ext(p))
		if srcExts[ext] {
			n++
			byExt[ext]++
			if n >= 30000 {
				return errStopWalk
			}
		}
		return nil
	})
	best, bestN := "", 0
	for ext, c := range byExt {
		if c > bestN {
			best, bestN = ext, c
		}
	}
	return n, extLang[best]
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
	return listDirs(allProjectDirs(cwd, func(agent codingAgent) []string {
		var dirs []string
		for _, d := range append(agent.ProjectDirs, agent.UserDirs...) {
			if strings.Contains(d, string(filepath.Separator)+"skills") {
				dirs = append(dirs, d)
			}
		}
		return dirs
	}))
}

func listAgents(cwd string) string {
	return listDirs(allProjectDirs(cwd, func(agent codingAgent) []string {
		var dirs []string
		for _, d := range append(agent.ProjectDirs, agent.UserDirs...) {
			if strings.Contains(d, string(filepath.Separator)+"agents") || strings.HasSuffix(d, string(filepath.Separator)+".agents") {
				dirs = append(dirs, d)
			}
		}
		return dirs
	}))
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
	var paths []string
	for _, agent := range codingAgents(cwd) {
		paths = append(paths, agent.MCPFiles...)
	}
	for _, p := range paths {
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
	return sortedNames(set)
}

func listPlugins(cwd string) string {
	set := map[string]bool{}
	for _, root := range allProjectDirs(cwd, func(agent codingAgent) []string {
		var dirs []string
		for _, d := range append(agent.ProjectDirs, agent.UserDirs...) {
			if strings.Contains(d, string(filepath.Separator)+"plugins") || strings.Contains(d, string(filepath.Separator)+"skills") {
				dirs = append(dirs, d)
			}
		}
		return dirs
	}) {
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
	return sortedNames(set)
}

func homeClaudeJSON() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}

func bumpCounter(sid string) int {
	if sid == "" {
		sid = "x"
	}
	p := filepath.Join(cockpitDir(), ".sa-count-"+sessionKey(sid))
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

// spawnWorker hands advisor work to the persistent daemon or a one-shot worker.
func spawnWorker(signals, session, cwd string) {
	dispatchAdvisor(signals, session, cwd)
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
	writeDebriefNote(in.SessionID)
	appendHistory(in.SessionID)
	// stateFile holds cross-session rate-limit metadata (5h/7d) — a new session
	// needs it immediately, so it survives SessionEnd. Everything session-scoped
	// (report, snapshot, chime, signals, counter) dies with the session so a
	// later session can never pick up this one's suggestions.
	paths := []string{
		sessionSignalsFile(in.SessionID),
		sessionReportFile(in.SessionID),
		sessionSnapshotFile(in.SessionID),
		sessionChimeFile(in.SessionID),
		sessionSeenFile(in.SessionID),
		filepath.Join(cockpitDir(), ".sa-count-"+sessionKey(in.SessionID)),
	}
	paths = append(paths, legacyGlobalFiles()...)
	for _, p := range paths {
		_ = os.Remove(p)
	}
	// Keep the .log itself as the durable record; the user can prune cockpit-logs.
}

func writeDebriefNote(session string) {
	if session == "" {
		return
	}
	var b strings.Builder
	RunDebrief(&b, session)
	logf(session, "debrief:\n%s", strings.TrimSpace(b.String()))
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
