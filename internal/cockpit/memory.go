package cockpit

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMemoryInterval = time.Hour
	maxMemoryReadBytes    = 1 << 20
	maxMemoryFiles        = 500
	defaultMemoryEntries  = 1000
)

type memoryEntry struct {
	TS      string   `json:"ts"`
	Agent   string   `json:"agent"`
	Session string   `json:"session"`
	Cwd     string   `json:"cwd,omitempty"`
	Source  string   `json:"source"`
	Summary string   `json:"summary"`
	Prompts []string `json:"prompts,omitempty"`
	Tools   []string `json:"tools,omitempty"`
	Files   []string `json:"files,omitempty"`
}

type memoryState struct {
	Files map[string]memoryFileState `json:"files"`
}

type memoryFileState struct {
	ModUnix int64 `json:"mod_unix"`
	Size    int64 `json:"size"`
}

type sessionRoot struct {
	Agent string
	Path  string
}

func memoryFile() string      { return filepath.Join(cockpitDir(), "memory.jsonl") }
func memoryStateFile() string { return filepath.Join(cockpitDir(), "memory-state.json") }

func memoryInterval() time.Duration {
	if v := os.Getenv("COCKPIT_MEMORY_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultMemoryInterval
}

func memoryLoop(ctx context.Context) {
	if os.Getenv("COCKPIT_MEMORY_DISABLE") == "1" {
		return
	}
	_ = RunMemoryScan()
	tick := time.NewTicker(memoryInterval())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := RunMemoryScan(); err != nil {
				daemonLog("memory: scan failed: %v", err)
			}
		}
	}
}

func RunMemoryScan() error {
	state := readMemoryState()
	entries := []memoryEntry{}
	for _, root := range memoryRoots() {
		files := memorySessionFiles(root.Path)
		for _, path := range files {
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			prev := state.Files[path]
			if prev.ModUnix == info.ModTime().Unix() && prev.Size == info.Size() {
				continue
			}
			entry, ok := summarizeSessionFile(root.Agent, path, info)
			state.Files[path] = memoryFileState{ModUnix: info.ModTime().Unix(), Size: info.Size()}
			if ok {
				entries = append(entries, entry)
			}
		}
	}
	if len(entries) > 0 {
		if err := appendMemoryEntries(entries); err != nil {
			return err
		}
		if err := compactMemory(defaultMemoryEntries); err != nil {
			return err
		}
	}
	return writeMemoryState(state)
}

func memoryRoots() []sessionRoot {
	var roots []sessionRoot
	add := func(agent, path string) {
		if path != "" {
			roots = append(roots, sessionRoot{agent, path})
		}
	}
	if d := os.Getenv("COCKPIT_CLAUDE_SESSION_DIR"); d != "" {
		add("claude", d)
	} else {
		add("claude", filepath.Join(ConfigDir(), "projects"))
	}
	if d := os.Getenv("COCKPIT_CODEX_SESSION_DIR"); d != "" {
		add("codex", d)
	} else {
		add("codex", filepath.Join(CodexConfigDir(), "sessions"))
	}
	if d := os.Getenv("COCKPIT_CURSOR_SESSION_DIR"); d != "" {
		add("cursor", d)
	} else {
		add("cursor", filepath.Join(CursorConfigDir(), "sessions"))
		if home, err := os.UserHomeDir(); err == nil {
			add("cursor", filepath.Join(home, "Library", "Application Support", "Cursor", "User", "workspaceStorage"))
		}
	}
	return roots
}

func memorySessionFiles(root string) []string {
	var files []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "Cache", "CachedData", "GPUCache":
				return fs.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".jsonl" || ext == ".json" || ext == ".log" {
			files = append(files, path)
			if len(files) >= maxMemoryFiles {
				return errStopWalk
			}
		}
		return nil
	})
	sort.Strings(files)
	return files
}

func summarizeSessionFile(agent, path string, info os.FileInfo) (memoryEntry, bool) {
	b, err := readTail(path, maxMemoryReadBytes)
	if err != nil || len(strings.TrimSpace(string(b))) == 0 {
		return memoryEntry{}, false
	}
	prompts, tools, files, cwd := extractMemorySignals(b)
	if len(prompts) == 0 && len(tools) == 0 && len(files) == 0 {
		return memoryEntry{}, false
	}
	session := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	entry := memoryEntry{
		TS:      info.ModTime().Format(time.RFC3339),
		Agent:   agent,
		Session: safeSession(session),
		Cwd:     cwd,
		Source:  path,
		Prompts: lastN(prompts, 3),
		Tools:   tools,
		Files:   files,
	}
	entry.Summary = buildMemorySummary(entry)
	return entry, true
}

func readTail(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxBytes {
		if _, err := f.Seek(-maxBytes, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(io.LimitReader(f, maxBytes))
}

func extractMemorySignals(b []byte) (prompts, tools, files []string, cwd string) {
	toolCounts := map[string]int{}
	fileSet := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e tEntry
		if json.Unmarshal([]byte(line), &e) == nil && (e.Message.Role != "" || len(e.Message.Content) > 0) {
			ps, ts, fs, lineCwd := memoryFromClaudeEntry(e)
			prompts = append(prompts, ps...)
			for _, t := range ts {
				toolCounts[t]++
			}
			for _, f := range fs {
				fileSet[f] = true
			}
			if cwd == "" {
				cwd = lineCwd
			}
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			if p := genericPrompt(m); p != "" {
				prompts = append(prompts, p)
			}
			if t := genericString(m, "tool", "name", "command"); t != "" {
				toolCounts[t]++
			}
			if f := genericString(m, "file", "file_path", "path"); f != "" {
				fileSet[f] = true
			}
			if cwd == "" {
				cwd = genericString(m, "cwd", "workspace", "current_dir")
			}
		}
	}
	if len(prompts) == 0 {
		prompts = extractJSONTextPrompts(string(b))
	}
	for name, n := range toolCounts {
		tools = append(tools, fmt.Sprintf("%s:%d", compactText(name, 40), n))
	}
	sort.Strings(tools)
	for f := range fileSet {
		files = append(files, compactPath(f))
	}
	sort.Strings(files)
	if len(files) > 8 {
		files = files[len(files)-8:]
	}
	return prompts, tools, files, cwd
}

func memoryFromClaudeEntry(e tEntry) (prompts, tools, files []string, cwd string) {
	var items []contentItem
	if json.Unmarshal(e.Message.Content, &items) == nil {
		for _, it := range items {
			switch it.Type {
			case "text":
				if e.Message.Role == "user" && it.Text != "" {
					prompts = append(prompts, compactText(redactSecrets(it.Text), 240))
				}
			case "tool_use":
				if it.Name != "" {
					tools = append(tools, it.Name)
				}
				if it.Input.FilePath != "" {
					files = append(files, it.Input.FilePath)
				}
			}
		}
		return prompts, tools, files, cwd
	}
	var s string
	if json.Unmarshal(e.Message.Content, &s) == nil && e.Message.Role == "user" {
		prompts = append(prompts, compactText(redactSecrets(s), 240))
	}
	return prompts, tools, files, cwd
}

func genericPrompt(m map[string]any) string {
	role := strings.ToLower(genericString(m, "role", "speaker", "author"))
	if role != "" && role != "user" && role != "human" {
		return ""
	}
	return compactText(redactSecrets(genericString(m, "prompt", "text", "content", "message")), 240)
}

func genericString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func extractJSONTextPrompts(s string) []string {
	var out []string
	for _, marker := range []string{`"role":"user"`, `"role": "user"`} {
		if !strings.Contains(s, marker) {
			continue
		}
		for _, part := range strings.Split(s, marker) {
			if i := strings.Index(part, `"text"`); i >= 0 {
				if p := quotedValueAfter(part[i:], ":"); p != "" {
					out = append(out, compactText(redactSecrets(p), 240))
				}
			}
		}
	}
	return out
}

func quotedValueAfter(s, sep string) string {
	i := strings.Index(s, sep)
	if i < 0 {
		return ""
	}
	s = strings.TrimSpace(s[i+len(sep):])
	if !strings.HasPrefix(s, `"`) {
		return ""
	}
	var out strings.Builder
	esc := false
	for _, r := range s[1:] {
		if esc {
			out.WriteRune(r)
			esc = false
			continue
		}
		if r == '\\' {
			esc = true
			continue
		}
		if r == '"' {
			break
		}
		out.WriteRune(r)
	}
	return out.String()
}

func buildMemorySummary(e memoryEntry) string {
	var parts []string
	if len(e.Prompts) > 0 {
		parts = append(parts, "user asked: "+e.Prompts[len(e.Prompts)-1])
	}
	if len(e.Tools) > 0 {
		parts = append(parts, "tools "+strings.Join(e.Tools, " "))
	}
	if len(e.Files) > 0 {
		parts = append(parts, "files "+strings.Join(e.Files, " "))
	}
	if len(parts) == 0 {
		return "session activity recorded"
	}
	return compactText(strings.Join(parts, "; "), 500)
}

func appendMemoryEntries(entries []memoryEntry) error {
	if err := os.MkdirAll(cockpitDir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(memoryFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

func readMemoryState() memoryState {
	st := memoryState{Files: map[string]memoryFileState{}}
	b, err := os.ReadFile(memoryStateFile())
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, &st)
	if st.Files == nil {
		st.Files = map[string]memoryFileState{}
	}
	return st
}

func writeMemoryState(st memoryState) error {
	if err := os.MkdirAll(cockpitDir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(memoryStateFile(), append(b, '\n'), 0o644)
}

func compactMemory(maxEntries int) error {
	if maxEntries <= 0 {
		maxEntries = defaultMemoryEntries
	}
	entries := readMemoryEntries("", maxEntries)
	if len(entries) == 0 {
		return nil
	}
	tmp := memoryFile() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, memoryFile())
}

func readMemoryEntries(query string, limit int) []memoryEntry {
	b, err := os.ReadFile(memoryFile())
	if err != nil {
		return nil
	}
	var entries []memoryEntry
	q := strings.ToLower(strings.TrimSpace(query))
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var e memoryEntry
		if json.Unmarshal([]byte(ln), &e) != nil {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.Agent+" "+e.Session+" "+e.Cwd+" "+e.Summary+" "+strings.Join(e.Prompts, " ")), q) {
			continue
		}
		entries = append(entries, e)
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries
}

func RunMemory(w io.Writer, query string, limit int, jsonOut bool) {
	entries := readMemoryEntries(query, limit)
	if jsonOut {
		enc := json.NewEncoder(w)
		for _, e := range entries {
			_ = enc.Encode(e)
		}
		return
	}
	if len(entries) == 0 {
		fmt.Fprintln(w, "No cockpit memory entries.")
		return
	}
	for _, e := range entries {
		fmt.Fprintf(w, "%s  %-6s  %s\n", e.TS, e.Agent, e.Summary)
		if e.Cwd != "" {
			fmt.Fprintf(w, "  cwd: %s\n", e.Cwd)
		}
	}
}

func compactText(s string, max int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if max > 0 && len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

func compactPath(path string) string {
	if path == "" {
		return ""
	}
	if base := filepath.Base(path); base != "." && base != string(filepath.Separator) {
		return base
	}
	return path
}

func lastN(in []string, n int) []string {
	if n <= 0 || len(in) <= n {
		return in
	}
	return in[len(in)-n:]
}
