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

type Signals struct {
	Turns               int
	Model               string
	ApproxContextTokens int64
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
		return
	}

	signals := formatSignals(collectSignals(in, n))
	spawnWorker(signals)
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

	return Signals{
		Turns:               turns,
		Model:               fallback(lastModel, "?"),
		ApproxContextTokens: ctxTokens,
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
	return fmt.Sprintf(`turns=%d  model=%s  approx_context_tokens=%d
tool_histogram: %s
searches=%d  files_reread_3x+=%d
graphify_graph=%s  repo_source_files=%s  est_graph_build=%s
available_skills: %s
available_agents: %s
available_mcp_servers: %s
available_plugins: %s
recent_prompts: %s`,
		s.Turns, fallback(s.Model, "?"), s.ApproxContextTokens,
		histString(s.ToolHistogram), s.Searches, s.FilesReread3x,
		graph, s.RepoSourceFiles, s.EstGraphBuild,
		s.AvailableSkills,
		s.AvailableAgents,
		s.AvailableMCPServers,
		s.AvailablePlugins,
		strings.Join(s.RecentPrompts, " "))
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

// spawnWorker writes the signals to a temp file and starts `cockpit worker` as a
// fully detached process (own process group, /dev/null fds) so it outlives this
// hook and never blocks the turn.
func spawnWorker(signals string) {
	tmp, err := os.CreateTemp("", "cockpit-sig-*")
	if err != nil {
		debugLog("spawnWorker: temp file: %v", err)
		return
	}
	_, _ = tmp.WriteString(signals)
	_ = tmp.Close()

	exe, err := os.Executable()
	if err != nil {
		debugLog("spawnWorker: executable: %v", err)
		return
	}
	cmd := exec.Command(exe, "worker", tmp.Name())
	cmd.Env = append(os.Environ(), "MODEL_HINT_GUARD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	}
	if err := cmd.Start(); err != nil {
		debugLog("spawnWorker: start: %v", err)
	}
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
