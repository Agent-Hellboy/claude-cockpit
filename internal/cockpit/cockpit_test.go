package cockpit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// strip ANSI for readable assertions.
func plain(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestFmtTokens(t *testing.T) {
	cases := map[int64]string{0: "0", 999: "999", 1500: "1k", 156000: "156k", 1000000: "1.0M", 1250000: "1.2M"}
	for in, want := range cases {
		if got := fmtTokens(in); got != want {
			t.Errorf("fmtTokens(%d)=%q want %q", in, got, want)
		}
	}
}

func TestGauge(t *testing.T) {
	if got := gauge(0); got != strings.Repeat("░", 10) {
		t.Errorf("gauge(0)=%q", got)
	}
	if got := gauge(100); got != strings.Repeat("█", 10) {
		t.Errorf("gauge(100)=%q", got)
	}
	if got := gauge(150); got != strings.Repeat("█", 10) {
		t.Errorf("gauge over 100 should clamp: %q", got)
	}
	if got := gauge(50); got != strings.Repeat("█", 5)+strings.Repeat("░", 5) {
		t.Errorf("gauge(50)=%q", got)
	}
	// eighth-block partial: 25%% -> 2 full + a half cell + 7 empty.
	if got := gauge(25); got != "██▌░░░░░░░" {
		t.Errorf("gauge(25)=%q want sub-cell partial", got)
	}
}

func TestEmojiLines(t *testing.T) {
	in := "Here are tips:\n🎯 **Switch model** now\n\n📖 Stop re-reading\nplain prose line\n🔍 use graph\n💰 more"
	got := emojiLines(in, 3)
	if len(got) != 3 {
		t.Fatalf("want 3 lines got %d: %v", len(got), got)
	}
	if strings.Contains(got[0], "**") {
		t.Errorf("markdown not stripped: %q", got[0])
	}
	if got[0] != "🎯 Switch model now" || got[2] != "🔍 use graph" {
		t.Errorf("unexpected: %v", got)
	}
	for _, l := range got {
		if strings.HasPrefix(l, "plain") || strings.HasPrefix(l, "Here") {
			t.Errorf("prose leaked: %q", l)
		}
	}
}

func TestRenderStatuslineNearFull(t *testing.T) {
	t.Setenv("COLUMNS", "140") // full width: assert every segment renders (no responsive drop)
	var in slInput
	in.Model.DisplayName = "Opus 4.8 (1M context)"
	in.Effort.Level = "high"
	in.Workspace.CurrentDir = "/x/mcp-runtime"
	in.Worktree.Branch = "main"
	in.ContextWindow.UsedPercentage = 99
	in.ContextWindow.TotalInputTokens = 985000
	in.ContextWindow.ContextWindowSize = 1000000
	in.Cost.TotalCostUSD = 24.3
	rows := renderStatusline(in, nil, cockpitSnapshot{Phase: "cruise"}, cockpitState{})
	if len(rows) < 2 {
		t.Fatalf("want at least 2 rows, got %d", len(rows))
	}
	p := plain(rows[0])
	for _, want := range []string{"mcp-runtime", "⎇main", "Opus 4.8 (1M context)", "high", "ctx", "99%", "985k/1.0M", "⚠ /compact"} {
		if !strings.Contains(p, want) {
			t.Errorf("row1 missing %q: %s", want, p)
		}
	}
}

// Regression: past 200k tokens on a large (1M) window must NOT fire /compact —
// exceeds_200k_tokens is true there but the window is only ~21% full.
func TestRenderStatuslineNoEarlyCompactWarn(t *testing.T) {
	var in slInput
	in.Workspace.CurrentDir = "/x/repo"
	in.ContextWindow.UsedPercentage = 21
	in.ContextWindow.TotalInputTokens = 211000
	in.ContextWindow.ContextWindowSize = 1000000
	in.Exceeds200k = true
	p := plain(renderStatusline(in, nil, cockpitSnapshot{}, cockpitState{})[0])
	if strings.Contains(p, "/compact") {
		t.Errorf("should not warn at 21%%: %s", p)
	}

	// ...but at 90%+ it must warn, with the emoji-presentation glyph.
	in.ContextWindow.UsedPercentage = 92
	p = plain(renderStatusline(in, nil, cockpitSnapshot{}, cockpitState{})[0])
	if !strings.Contains(p, "⚠ /compact") {
		t.Errorf("want compact warn at 92%%: %s", p)
	}
}

func TestRenderStatuslinePRAndHint(t *testing.T) {
	var in slInput
	in.Workspace.CurrentDir = "/x/repo"
	in.Worktree.Branch = "feat"
	in.PR.Number = json.Number("336")
	in.PR.ReviewState = "APPROVED"
	rows := renderStatusline(in, []classifiedSuggestion{
		{Level: AlertMemo, Text: "✅ session looks efficient."},
		{Level: AlertAdv, Text: "💡 use sonnet"},
	}, cockpitSnapshot{Phase: "cruise"}, cockpitState{})
	if len(rows) != 6 {
		t.Fatalf("want 6 rows (2 instruments+advisor header+note+fix+apply), got %d", len(rows))
	}
	if !strings.Contains(plain(rows[0]), "● steady") {
		t.Errorf("phase badge missing: %s", plain(rows[0]))
	}
	if !strings.Contains(plain(rows[0]), "⇡#336") {
		t.Errorf("PR segment missing: %s", plain(rows[0]))
	}
	if !strings.Contains(plain(rows[2]), "advisor") {
		t.Errorf("advisor section header missing: %q", plain(rows[2]))
	}
	if !strings.Contains(plain(rows[3]), "note") || !strings.Contains(plain(rows[3]), "efficient") {
		t.Errorf("note row wrong: %q", plain(rows[3]))
	}
	if strings.Contains(plain(rows[3]), "cockpit apply") {
		t.Errorf("note should not have apply: %q", plain(rows[3]))
	}
	if !strings.Contains(plain(rows[4]), "1") || !strings.Contains(plain(rows[4]), "use sonnet") {
		t.Errorf("fix row wrong: %q", plain(rows[4]))
	}
	if !strings.Contains(plain(rows[5]), "cockpit apply 1") {
		t.Errorf("apply line missing: %q", plain(rows[5]))
	}
}

func TestIsApplyable(t *testing.T) {
	if isApplyable(classifiedSuggestion{Level: AlertMemo, Text: "✅ session looks efficient."}) {
		t.Fatal("memo should not be applyable")
	}
	if !isApplyable(classifiedSuggestion{Level: AlertAdv, Text: "🔌 Audit Playwright MCP for screenshots"}) {
		t.Fatal("MCP tip should be applyable")
	}
	if isApplyable(classifiedSuggestion{Level: AlertWarn, Text: "⚠️ Context critical — run /compact"}) {
		t.Fatal("/compact warn should not be applyable")
	}
}

func TestRunStatuslineSmoke(t *testing.T) {
	in := `{"model":{"display_name":"Sonnet 4.6"},"workspace":{"current_dir":"/tmp/foo"},"context_window":{"used_percentage":47,"total_input_tokens":472000,"context_window_size":1000000}}`
	var out bytes.Buffer
	RunStatusline(strings.NewReader(in), &out)
	if !strings.Contains(plain(out.String()), "Sonnet 4.6") {
		t.Errorf("smoke output: %s", out.String())
	}
}

func TestGraphETA(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{{100, "~1-2"}, {500, "~2-4"}, {2000, "~4-8"}, {5000, "~8-15"}, {9000, "15+"}}
	for _, c := range cases {
		if got := graphETA(c.n); !strings.Contains(got, c.want) {
			t.Errorf("graphETA(%d)=%q want contains %q", c.n, got, c.want)
		}
	}
}

func TestCountSourceFiles(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a.go", "b.go", "c.ts", "d.txt", "README.md"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "skip.go"), []byte("x"), 0o644)
	if got := countSourceFiles(dir); got != 3 {
		t.Errorf("countSourceFiles=%d want 3 (.go,.go,.ts; node_modules skipped)", got)
	}
}

func TestSettingsMergePreservesAndDedups(t *testing.T) {
	// existing settings: other keys + foreign Stop hook + old statusLine.
	m := map[string]any{
		"model":       "opus",
		"permissions": map[string]any{"allow": []any{"Bash(ls:*)"}},
		"statusLine":  map[string]any{"type": "command", "command": "/old/line.sh"},
		"hooks": map[string]any{
			"Stop": []any{
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/foreign/tool.sh"}}},
			},
		},
	}
	cmd := "'/x/cockpit' analyze"
	setEventHook(m, "Stop", cmd, "analyze")
	setEventHook(m, "Stop", cmd, "analyze") // twice -> must not duplicate

	stop := toList(m["hooks"].(map[string]any)["Stop"])
	cmds := map[string]int{}
	for _, g := range stop {
		for _, h := range toList(g.(map[string]any)["hooks"]) {
			cmds[h.(map[string]any)["command"].(string)]++
		}
	}
	if cmds["/foreign/tool.sh"] != 1 {
		t.Errorf("foreign hook not preserved: %v", cmds)
	}
	if cmds[cmd] != 1 {
		t.Errorf("cockpit hook should appear exactly once, got %d", cmds[cmd])
	}
	if m["model"] != "opus" {
		t.Error("unrelated key 'model' lost")
	}

	// uninstall removes only ours, keeps foreign + other keys.
	removeEventHook(m, "Stop", cmd, "analyze")
	delete(m, "statusLine") // simulate Uninstall's statusLine drop
	stop = toList(m["hooks"].(map[string]any)["Stop"])
	if len(stop) != 1 {
		t.Fatalf("want 1 remaining (foreign) group, got %d", len(stop))
	}
	if m["model"] != "opus" {
		t.Error("uninstall dropped unrelated key")
	}
}

func TestQuoteShellPath(t *testing.T) {
	got := quote("/Users/o'connor/Claude Tools/cockpit")
	want := "'/Users/o'\\''connor/Claude Tools/cockpit'"
	if got != want {
		t.Fatalf("quote=%q want %q", got, want)
	}
}

func TestSetStopHookReplacesOldCockpitPath(t *testing.T) {
	m := map[string]any{
		"hooks": map[string]any{
			"Stop": []any{
				map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": quote("/old path/cockpit") + " analyze"},
					map[string]any{"type": "command", "command": "/foreign/tool.sh"},
				}},
			},
		},
	}
	setEventHook(m, "Stop", quote("/new path/o'connor/cockpit")+" analyze", "analyze")

	stop := toList(m["hooks"].(map[string]any)["Stop"])
	cmds := map[string]int{}
	for _, g := range stop {
		for _, h := range toList(g.(map[string]any)["hooks"]) {
			cmds[h.(map[string]any)["command"].(string)]++
		}
	}
	if cmds[quote("/old path/cockpit")+" analyze"] != 0 {
		t.Fatalf("old cockpit hook was not removed: %v", cmds)
	}
	if cmds[quote("/new path/o'connor/cockpit")+" analyze"] != 1 {
		t.Fatalf("new cockpit hook missing: %v", cmds)
	}
	if cmds["/foreign/tool.sh"] != 1 {
		t.Fatalf("foreign hook not preserved: %v", cmds)
	}
}

func TestMalformedStopHookReplaced(t *testing.T) {
	m := map[string]any{"hooks": map[string]any{"Stop": map[string]any{"bad": true}}}
	setEventHook(m, "Stop", quote("/x/cockpit")+" analyze", "analyze")
	stop := toList(m["hooks"].(map[string]any)["Stop"])
	if len(stop) != 1 {
		t.Fatalf("want one hook group, got %#v", stop)
	}
}

func TestInstallCodexAndCursorProjectFiles(t *testing.T) {
	dir := t.TempDir()
	// Exercise the target helpers directly so the test does not chdir the whole
	// process or write AGENTS.md/.cursor into the checked-out repository.
	if err := installCodex(dir); err != nil {
		t.Fatal(err)
	}
	if err := installCursor(dir); err != nil {
		t.Fatal(err)
	}
	if err := installCodex(dir); err != nil {
		t.Fatal(err)
	}
	if err := installCursor(dir); err != nil {
		t.Fatal(err)
	}

	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(agents), managedStart("codex")) != 1 || !strings.Contains(string(agents), "cockpit systems") {
		t.Fatalf("Codex AGENTS.md install not idempotent/useful: %s", agents)
	}
	skill, err := os.ReadFile(filepath.Join(dir, ".codex", "skills", "agent-flightdeck", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(skill), "cockpit apply <n> --dry-run") {
		t.Fatalf("Codex skill missing cockpit workflow: %s", skill)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "skills", "agent-flightdeck", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("Codex install should not write Claude skill: %v", err)
	}
	cursor, err := os.ReadFile(filepath.Join(dir, ".cursor", "rules", "agent-flightdeck.mdc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(cursor), "---\n") || !strings.Contains(string(cursor), "---\n\n"+managedStart("cursor")) || strings.Count(string(cursor), managedStart("cursor")) != 1 || !strings.Contains(string(cursor), "alwaysApply: true") {
		t.Fatalf("Cursor rule install not idempotent/useful: %s", cursor)
	}

	if err := uninstallCodex(dir); err != nil {
		t.Fatal(err)
	}
	if err := uninstallCursor(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".codex", "skills", "agent-flightdeck", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("Codex skill should be removed on uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".cursor", "rules", "agent-flightdeck.mdc")); !os.IsNotExist(err) {
		t.Fatalf("Cursor rule should be removed on uninstall: %v", err)
	}
}

func TestTailEntriesBounded(t *testing.T) {
	dir := t.TempDir()
	tp := filepath.Join(dir, "t.jsonl")
	var b strings.Builder
	for i := 0; i < 5; i++ {
		b.WriteString(`{"message":{"role":"user","content":"prompt ` + string(rune('0'+i)) + `"}}` + "\n")
	}
	if err := os.WriteFile(tp, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got := tailEntries(tp, 2)
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	var s string
	if err := json.Unmarshal(got[0].Message.Content, &s); err != nil || s != "prompt 3" {
		t.Fatalf("first tail entry=%q err=%v", s, err)
	}
	if err := json.Unmarshal(got[1].Message.Content, &s); err != nil || s != "prompt 4" {
		t.Fatalf("second tail entry=%q err=%v", s, err)
	}
}

func TestGatherSignalsAndCadence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	// build a transcript: 5 grep Bash uses, same file read 3x, a user prompt.
	var b strings.Builder
	for i := 0; i < 5; i++ {
		b.WriteString(`{"message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":1000,"cache_read_input_tokens":410000},"content":[{"type":"tool_use","name":"Bash","input":{"command":"grep -rn x ."}}]}}` + "\n")
	}
	for i := 0; i < 3; i++ {
		b.WriteString(`{"message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/r/big.go"}}]}}` + "\n")
	}
	b.WriteString(`{"message":{"role":"user","content":"rename this field"}}` + "\n")
	tp := filepath.Join(dir, "t.jsonl")
	os.WriteFile(tp, []byte(b.String()), 0o644)
	os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers":{"github":{},"context7":{}}}`), 0o644)
	os.MkdirAll(filepath.Join(dir, ".claude", "agents", "reviewer"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".claude", "skills", "superclaude", ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(dir, ".claude", "skills", "superclaude", ".claude-plugin", "plugin.json"), []byte(`{"name":"superclaude"}`), 0o644)

	in := stopInput{TranscriptPath: tp, Cwd: dir, SessionID: "s"}
	sig := gatherSignals(in, 30)
	for _, want := range []string{
		"searches=5", "files_reread_3x+=1", "graphify_graph=no", "model=claude-opus-4-8",
		"Bash:5", "Read:3", "available_agents: reviewer", "available_mcp_servers: context7 github",
		"available_plugins: superclaude", "rename this field",
	} {
		if !strings.Contains(sig, want) {
			t.Errorf("signals missing %q:\n%s", want, sig)
		}
	}

	b.WriteString(`{"message":{"role":"user","content":"api_key=abc123 password:secret"}}` + "\n")
	os.WriteFile(tp, []byte(b.String()), 0o644)
	sig = gatherSignals(in, 31)
	if strings.Contains(sig, "abc123") || strings.Contains(sig, "password:secret") {
		t.Fatalf("secret was not redacted:\n%s", sig)
	}

	t.Setenv("COCKPIT_ANALYZE_PROMPTS", "0")
	sig = gatherSignals(in, 32)
	if strings.Contains(sig, "rename this field") {
		t.Fatalf("prompts should be omitted when disabled:\n%s", sig)
	}

	// cadence: counter is independent state per session id.
	if got := bumpCounter("c1"); got != 1 {
		t.Errorf("first bump=%d want 1", got)
	}
	if got := bumpCounter("c1"); got != 2 {
		t.Errorf("second bump=%d want 2", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "cockpit-logs", ".sa-count-c1")); !os.IsNotExist(err) {
		t.Fatalf("raw session id should not be used as filename")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "cockpit-logs", ".sa-count-*")); len(matches) != 1 {
		t.Fatalf("want one hashed counter file, got %v", matches)
	}
}

func TestMultiAgentDiscovery(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CURSOR_CONFIG_DIR", filepath.Join(home, ".cursor"))

	for _, p := range []string{
		filepath.Join(dir, ".claude", "agents", "reviewer"),
		filepath.Join(dir, ".codex", "agents", "planner"),
		filepath.Join(dir, ".agents", "legacy"),
		filepath.Join(dir, ".claude", "skills", "ship"),
		filepath.Join(dir, ".codex", "skills", "audit"),
		filepath.Join(dir, ".cursor", "rules", "frontend.mdc"),
	} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".cursor", "mcp.json"), []byte(`{"mcpServers":{"browser":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := listAgents(dir); !strings.Contains(got, "planner") || !strings.Contains(got, "reviewer") || !strings.Contains(got, "legacy") {
		t.Fatalf("multi-agent discovery missed agents: %q", got)
	}
	if got := listSkills(dir); !strings.Contains(got, "audit") || !strings.Contains(got, "ship") {
		t.Fatalf("multi-agent discovery missed skills: %q", got)
	}
	if got := listMCPServers(dir); !strings.Contains(got, "browser") {
		t.Fatalf("Cursor MCP discovery missed server: %q", got)
	}
	if got := listCodingAgents(dir); !strings.Contains(got, "claude:ready") || !strings.Contains(got, "codex:ready") || !strings.Contains(got, "cursor:ready") {
		t.Fatalf("coding agent summary wrong: %q", got)
	}
}

// The status line writes the authoritative context window; the analyzer should
// prefer it over inferring from the model name.
func TestContextWindowBridge(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	tp := filepath.Join(dir, "t.jsonl")
	os.WriteFile(tp, []byte(`{"message":{"role":"assistant","model":"claude-opus-4-8[1m]","content":[]}}`+"\n"), 0o644)
	in := stopInput{TranscriptPath: tp, Cwd: dir, SessionID: "s"}

	// No state file yet -> inferred from model name (1M for the [1m] variant).
	if got := collectSignals(in, 5); got.ContextSource != "inferred" || got.ContextWindow != 1_000_000 {
		t.Fatalf("inferred path: source=%s window=%d", got.ContextSource, got.ContextWindow)
	}

	// Simulate the status line capturing real data via writeState.
	var sl slInput
	sl.ContextWindow.ContextWindowSize = 200000
	sl.ContextWindow.UsedPercentage = 88
	sl.ContextWindow.TotalInputTokens = 176000
	sl.Cost.TotalCostUSD = 7.5
	sl.RateLimits.FiveHour.UsedPercentage = 91
	writeState(sl)

	got := collectSignals(in, 5)
	if got.ContextSource != "actual" {
		t.Errorf("want actual source, got %s", got.ContextSource)
	}
	if got.ContextWindow != 200000 || got.ContextUsedPct != 88 {
		t.Errorf("want real window 200000/88%%, got %d/%d", got.ContextWindow, got.ContextUsedPct)
	}
	if got.Rate5hPct != 91 || got.CostUSD != 7.5 {
		t.Errorf("rate/cost not bridged: 5h=%d cost=%.2f", got.Rate5hPct, got.CostUSD)
	}
	if sig := formatSignals(got); !strings.Contains(sig, "context_used_pct=88 (actual)") {
		t.Errorf("formatted signals missing actual pct:\n%s", sig)
	}
}

func TestWrapText(t *testing.T) {
	if got := wrapText("", 100); got != nil {
		t.Errorf("empty -> %v want nil", got)
	}
	long := "🎯 Model downgrade: switch to Sonnet because the recent prompts are all mechanical UI testing work"
	got := wrapText(long, 40)
	if len(got) < 2 {
		t.Fatalf("expected wrap into multiple lines, got %v", got)
	}
	for _, ln := range got {
		if utf8.RuneCountInString(ln) > 40 {
			t.Errorf("line exceeds width: %q (%d)", ln, utf8.RuneCountInString(ln))
		}
	}
	if strings.Join(got, " ") != long {
		t.Errorf("wrap lost/changed words: %q", strings.Join(got, " "))
	}
}

func TestExtractToolGap(t *testing.T) {
	// structured form: capability || evidence || query.
	out := "🎯 switch model\nTOOLGAP: browser automation || prompts ask for UI checks, curl used 12x || Playwright MCP Claude Code\n"
	g, ok := extractToolGap(out)
	if !ok || g.Need != "browser automation" || !strings.Contains(g.Evidence, "curl used 12x") ||
		g.Query != "Playwright MCP Claude Code" {
		t.Errorf("structured gap parse: ok=%v %+v", ok, g)
	}
	// legacy single-phrase form still parses, with a derived query.
	g, ok = extractToolGap("TOOLGAP: browser automation and screenshots")
	if !ok || g.Need != "browser automation and screenshots" || !strings.Contains(g.Query, "browser automation") {
		t.Errorf("legacy gap parse: ok=%v %+v", ok, g)
	}
	if _, ok := extractToolGap("🎯 all good\n✅ efficient"); ok {
		t.Error("no gap line should parse as a gap")
	}
}

// The scout prompt is targeted with the session's own analysis: gap evidence,
// stack, and already-installed integrations it must not re-suggest.
func TestBuildScoutPrompt(t *testing.T) {
	sig := "turns=9\nrepo_lang=Go  graphify_graph=yes  repo_source_files=?\n" +
		"available_skills: graphify verify\navailable_mcp_servers: github context7\n"
	got := buildScoutPrompt(toolGap{
		Need:     "browser automation",
		Evidence: "curl used for UI checks",
		Query:    "Playwright MCP",
	}, sig)
	for _, want := range []string{
		"CAPABILITY GAP: browser automation",
		"SESSION EVIDENCE: curl used for UI checks",
		"SUGGESTED SEARCH QUERY: Playwright MCP",
		"PROJECT STACK: Go",
		"github context7 graphify verify",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("scout prompt missing %q", want)
		}
	}
}

func TestScanSourceLang(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a.go", "b.go", "c.ts", "d.md"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	n, lang := scanSource(dir)
	if n != 3 || lang != "Go" {
		t.Fatalf("scanSource=%d,%q want 3,Go", n, lang)
	}
}

func TestReadSuggestionsBoundedAndStale(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	// six lines stored -> capped at 4 for the bar.
	many := []string{"ADV|1️⃣ a", "ADV|2️⃣ b", "ADV|3️⃣ c", "ADV|4️⃣ d", "ADV|5️⃣ e", "ADV|6️⃣ f"}
	if err := writeReportLines("sess-a", "/proj/a", many); err != nil {
		t.Fatal(err)
	}
	got := readSuggestions("sess-a")
	if len(got) != 4 {
		t.Fatalf("line cap: got %d lines want 4: %v", len(got), got)
	}
	if readSuggestions("other-session") != nil {
		t.Fatalf("another session must not see sess-a's report")
	}
	if readSuggestions("") != nil {
		t.Fatalf("empty session must read nothing")
	}

	stale := time.Now().Add(-hintMaxAge - time.Minute)
	if err := os.Chtimes(sessionReportFile("sess-a"), stale, stale); err != nil {
		t.Fatal(err)
	}
	if got := readSuggestions("sess-a"); got != nil {
		t.Fatalf("stale suggestions should be ignored, got %v", got)
	}
}

// Regression: suggestions from one session must never surface in another —
// previously a single global .session-report meant the daemon's last-processed
// session leaked its advice into every other session's status bar and apply.
func TestSuggestionSessionIsolation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	if err := writeSuggestionReport("sess-a", "/proj/a", []classifiedSuggestion{
		{Level: AlertCaut, Text: "🔄 mTLS scenario loop — use /loop"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeSuggestionReport("sess-b", "/proj/b", []classifiedSuggestion{
		{Level: AlertAdv, Text: "🔍 use graphify query"},
	}); err != nil {
		t.Fatal(err)
	}

	a, b := readSuggestions("sess-a"), readSuggestions("sess-b")
	if len(a) != 1 || !strings.Contains(a[0], "mTLS") {
		t.Fatalf("sess-a report wrong: %v", a)
	}
	if len(b) != 1 || strings.Contains(b[0], "mTLS") {
		t.Fatalf("sess-b leaked sess-a's suggestion: %v", b)
	}

	// snapshots are isolated the same way.
	writeSnapshot("sess-a", cockpitSnapshot{Cwd: "/proj/a", Phase: "emergency", ContextUsedPct: 95})
	writeSnapshot("sess-b", cockpitSnapshot{Cwd: "/proj/b", Phase: "cruise", ContextUsedPct: 10})
	if got := readSnapshot("sess-b"); got.Phase != "cruise" || got.ContextUsedPct != 10 {
		t.Fatalf("sess-b snapshot polluted: %+v", got)
	}
}

// Fault acquisition: failed tool_results are counted, attributed to their
// originating tool via tool_use id, and classified into the not-found family.
func TestCollectSignalsToolErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	var b strings.Builder
	// Read that fails not-found, twice; a Bash failure that is not not-found.
	b.WriteString(`{"message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/x/missing.go"}}]}}` + "\n")
	b.WriteString(`{"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"File does not exist."}]}}` + "\n")
	b.WriteString(`{"message":{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/x/gone.go"}}]}}` + "\n")
	b.WriteString(`{"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","is_error":true,"content":[{"type":"text","text":"no such file or directory"}]}]}}` + "\n")
	b.WriteString(`{"message":{"role":"assistant","content":[{"type":"tool_use","id":"t3","name":"Bash","input":{"command":"make test"}}]}}` + "\n")
	b.WriteString(`{"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t3","is_error":true,"content":"exit status 2: FAIL mtls_scenario"}]}}` + "\n")
	// a successful result must not count.
	b.WriteString(`{"message":{"role":"assistant","content":[{"type":"tool_use","id":"t4","name":"Bash","input":{"command":"ls"}}]}}` + "\n")
	b.WriteString(`{"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t4","content":"ok"}]}}` + "\n")
	tp := filepath.Join(dir, "t.jsonl")
	os.WriteFile(tp, []byte(b.String()), 0o644)

	got := collectSignals(stopInput{TranscriptPath: tp, Cwd: dir, SessionID: "s"}, 5)
	if got.ToolErrors != 3 || got.NotFoundErrors != 2 {
		t.Fatalf("faults: errors=%d notFound=%d want 3/2", got.ToolErrors, got.NotFoundErrors)
	}
	if !strings.Contains(got.ErrorTop, "Read:2") {
		t.Fatalf("error_top should attribute to Read: %q", got.ErrorTop)
	}
	if sig := formatSignals(got); !strings.Contains(sig, "tool_errors=3  not_found_errors=2") {
		t.Fatalf("signals missing fault line:\n%s", sig)
	}
}

func TestLeverKey(t *testing.T) {
	// same lever, different phrasing -> same key.
	a := leverKey("ADV|💰 Delegate log scanning to an Explore subagent (Haiku)")
	b := leverKey("🔍 Use the Explore subagent on Haiku for broad log reads")
	if a != b {
		t.Fatalf("rephrased same-lever suggestions must key equal: %q vs %q", a, b)
	}
	// different MCP integrations -> different keys.
	if leverKey("🔌 Audit Playwright MCP for screenshots") == leverKey("🔌 Audit Sentry MCP for error logs") {
		t.Fatal("playwright and sentry MCP suggestions must not collide")
	}
	// no known lever -> falls back to normalized text.
	if leverKey("✨ do something unusual") == leverKey("✨ do another thing entirely") {
		t.Fatal("distinct no-lever texts must not collide")
	}
}

// The advisor must not nag: a lever already suggested this session is muted on
// later runs, standing fixes persist until applied, and instrument notes are
// regenerated fresh each run.
func TestMergeSuggestionsDedup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	run1 := []classifiedSuggestion{
		{Level: AlertCaut, Text: "🔄 repeated mTLS runs — use /loop to batch them"},
		{Level: AlertWarn, Text: "⚠️ Context at 91% — run /compact now"}, // note: not applyable
	}
	stored := mergeSuggestions("s", "/proj/a", run1)
	if len(stored) != 2 || countApplyable(stored) != 1 {
		t.Fatalf("run1 stored=%d applyable=%d want 2/1", len(stored), countApplyable(stored))
	}

	// run 2: same /loop lever rephrased + a fresh graphify lever + the warn again.
	run2 := []classifiedSuggestion{
		{Level: AlertCaut, Text: "🔁 batch the repeated mTLS scenario with /loop"},
		{Level: AlertAdv, Text: "🔍 use graphify query for architecture questions"},
		{Level: AlertWarn, Text: "⚠️ Context at 93% — run /compact now"},
	}
	stored = mergeSuggestions("s", "/proj/a", run2)
	joined := strings.Join(rawTexts(stored), "\n")
	if strings.Count(joined, "/loop") != 1 {
		t.Fatalf("duplicate /loop lever must be muted:\n%s", joined)
	}
	if !strings.Contains(joined, "graphify") {
		t.Fatalf("fresh lever missing:\n%s", joined)
	}
	if strings.Count(joined, "/compact") != 1 || !strings.Contains(joined, "93%") {
		t.Fatalf("instrument warn should regenerate fresh, once:\n%s", joined)
	}

	// run 3: only the duplicate again -> standing fixes survive, no new dup.
	stored = mergeSuggestions("s", "/proj/a", []classifiedSuggestion{
		{Level: AlertCaut, Text: "🔄 use /loop for the mTLS scenario loop"},
	})
	joined = strings.Join(rawTexts(stored), "\n")
	if strings.Count(joined, "/loop") != 1 || !strings.Contains(joined, "graphify") {
		t.Fatalf("run3 merge wrong:\n%s", joined)
	}
}

func rawTexts(cs []classifiedSuggestion) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Text
	}
	return out
}

// SessionEnd records the session's final instruments in the black-box history,
// and debrief reports a 7-day baseline from it.
func TestHistoryBaseline(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	writeSnapshot("h1", cockpitSnapshot{Cwd: "/p", CostUSD: 3.5, ContextUsedPct: 40, ToolErrors: 6, NotFoundErrors: 2})
	appendHistory("h1")
	writeSnapshot("h2", cockpitSnapshot{Cwd: "/p", CostUSD: 1.5, ContextUsedPct: 20, ToolErrors: 4})
	appendHistory("h2")
	appendHistory("never-analyzed") // no snapshot -> not recorded

	n, cost, avgCtx, faults := baseline7d()
	if n != 2 || cost != 5.0 || avgCtx != 30 || faults != 10 {
		t.Fatalf("baseline: n=%d cost=%.2f avgCtx=%d faults=%d want 2/$5.00/30/10", n, cost, avgCtx, faults)
	}

	var out strings.Builder
	RunDebrief(&out, "h1")
	if !strings.Contains(out.String(), "baseline 7d: 2 sessions · $5.00 · avg ctx 30% · 10 faults") {
		t.Fatalf("debrief missing baseline:\n%s", out.String())
	}
}

func TestMemoryScanAndRetrieve(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, "claude")
	codexDir := filepath.Join(dir, "codex")
	cursorDir := filepath.Join(dir, "cursor")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CODEX_HOME", codexDir)
	t.Setenv("COCKPIT_CURSOR_SESSION_DIR", cursorDir)

	claudeProject := filepath.Join(claudeDir, "projects", "repo")
	if err := os.MkdirAll(claudeProject, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(codexDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeTranscript := strings.Join([]string{
		`{"message":{"role":"user","content":"implement oauth callback"}}`,
		`{"message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/repo/auth.go"}},{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(claudeProject, "claude-session.jsonl"), []byte(claudeTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "sessions", "codex-session.jsonl"), []byte(`{"role":"user","content":"review payment flow","cwd":"/repo","tool":"Read","file":"payments.go"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cursorDir, "cursor-session.jsonl"), []byte(`{"role":"user","text":"fix dashboard layout","tool":"edit","path":"dashboard.tsx"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RunMemoryScan(); err != nil {
		t.Fatal(err)
	}
	entries := readMemoryEntries("", 10)
	if len(entries) != 3 {
		t.Fatalf("want three agent memory entries, got %d: %+v", len(entries), entries)
	}
	agents := map[string]bool{}
	for _, e := range entries {
		agents[e.Agent] = true
		if e.Summary == "" || len(e.Summary) > 500 {
			t.Fatalf("memory summary should be compact and useful: %+v", e)
		}
	}
	for _, agent := range []string{"claude", "codex", "cursor"} {
		if !agents[agent] {
			t.Fatalf("missing %s memory entry: %+v", agent, entries)
		}
	}

	if err := RunMemoryScan(); err != nil {
		t.Fatal(err)
	}
	if got := readMemoryEntries("", 10); len(got) != 3 {
		t.Fatalf("unchanged sessions should not duplicate memory, got %d", len(got))
	}

	var human, jsonOut bytes.Buffer
	RunMemory(&human, "payment", 5, false)
	if !strings.Contains(human.String(), "review payment flow") {
		t.Fatalf("query did not retrieve matching memory:\n%s", human.String())
	}
	RunMemory(&jsonOut, "dashboard", 5, true)
	if !strings.Contains(jsonOut.String(), `"agent":"cursor"`) {
		t.Fatalf("json retrieval missing cursor memory:\n%s", jsonOut.String())
	}
}

func TestResolveSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	// explicit override wins.
	t.Setenv("COCKPIT_SESSION", "forced")
	if got := resolveSession("/proj/a"); got != "forced" {
		t.Fatalf("COCKPIT_SESSION override ignored: %q", got)
	}
	t.Setenv("COCKPIT_SESSION", "")

	// two live sessions in different projects -> cwd picks the right one, and a
	// third project gets NOTHING rather than someone else's session.
	if err := writeReportLines("sess-a", "/proj/a", []string{"ADV|🅰️ a"}); err != nil {
		t.Fatal(err)
	}
	if err := writeReportLines("sess-b", "/proj/b", []string{"ADV|🅱️ b"}); err != nil {
		t.Fatal(err)
	}
	if got := resolveSession("/proj/a"); got != "sess-a" {
		t.Fatalf("resolveSession(/proj/a)=%q want sess-a", got)
	}
	if got := resolveSession("/proj/b"); got != "sess-b" {
		t.Fatalf("resolveSession(/proj/b)=%q want sess-b", got)
	}
	if got := resolveSession("/proj/c"); got != "" {
		t.Fatalf("ambiguous cwd must resolve to no session, got %q", got)
	}

	// a single live session is unambiguous from anywhere.
	if err := os.Remove(sessionReportFile("sess-b")); err != nil {
		t.Fatal(err)
	}
	if got := resolveSession("/proj/c"); got != "sess-a" {
		t.Fatalf("single live session should resolve from any cwd, got %q", got)
	}
}

// Regression: a suggestion citing 5h-budget pressure was classified as an
// applyable standing fix (it mentioned "subagents"), so it kept showing at
// "97%" long after the 5-hour window reset to 3%. Budget/rate pressure must
// (a) never be a standing fix and (b) be dropped when gauges contradict it.
func TestStaleBudgetSuggestionExpires(t *testing.T) {
	line := "WARN|⚠️ 5-hour budget at 97% — you've spent $170.06 and are near the limit; switch to Haiku for subagents or delegation now, or focus on wrapping up before you hit the ceiling."

	// (a) not applyable — it is an instrument warning, regenerated each run.
	if isApplyable(classifySuggestion(line, cockpitSnapshot{}, cockpitState{})) {
		t.Fatal("budget-pressure line must not be a standing applyable fix")
	}

	// (b) window reset: 5h now 3% -> the stored line is dropped at read time.
	fresh := cockpitSnapshot{Rate5hPct: 3, Rate7dPct: 32, ContextUsedPct: 51}
	got := parseSuggestionStore([]string{line}, 0, fresh, cockpitState{})
	if len(got) != 0 {
		t.Fatalf("stale 97%% claim should be dropped at 5h=3%%: %v", got)
	}

	// still under pressure -> the line stays.
	hot := cockpitSnapshot{Rate5hPct: 96, Rate7dPct: 40}
	if got := parseSuggestionStore([]string{line}, 0, hot, cockpitState{}); len(got) != 1 {
		t.Fatalf("valid pressure warn must survive: %v", got)
	}

	// context claims expire the same way; low-percent lines never do.
	ctxLine := "WARN|⚠️ Context at 92% — run /compact now"
	if got := parseSuggestionStore([]string{ctxLine}, 0, cockpitSnapshot{ContextUsedPct: 12}, cockpitState{}); len(got) != 0 {
		t.Fatalf("stale ctx claim should be dropped: %v", got)
	}
	benign := "ADV|🔍 502 source files with no graphify graph built; 181 searches logged (up 20%)"
	if got := parseSuggestionStore([]string{benign}, 0, fresh, cockpitState{}); len(got) != 1 {
		t.Fatalf("non-pressure line must survive: %v", got)
	}
}

// The expiry rule is disagreement with live gauges, not an absolute threshold:
// "rate climbing to 73% in 5h" must vanish after the window resets to 3%, stay
// while the gauge is near 73%, and notes without any claim age out on TTL.
func TestMidPressureClaimAndNoteTTL(t *testing.T) {
	climb := "CAUT|📈 Rate climbing to 73% in 5h — 15 searches + 26 Bash calls; if the next 2h shows similar velocity, switch to Sonnet to stretch the budget."

	// gauge agrees (within tolerance) -> stays.
	if got := parseSuggestionStore([]string{climb}, 0, cockpitSnapshot{Rate5hPct: 68}, cockpitState{}); len(got) != 1 {
		t.Fatalf("agreeing 73%% claim must survive at 5h=68%%: %v", got)
	}
	// window reset -> dropped, even though 73 < the old 75 threshold.
	if got := parseSuggestionStore([]string{climb}, 0, cockpitSnapshot{Rate5hPct: 3}, cockpitState{}); len(got) != 0 {
		t.Fatalf("73%% claim must be dropped at 5h=3%%: %v", got)
	}
	// pressure ROSE well past the claim -> also dropped (advisor will rewarn).
	if got := parseSuggestionStore([]string{climb}, 0, cockpitSnapshot{Rate5hPct: 97}, cockpitState{}); len(got) != 0 {
		t.Fatalf("under-warning 73%% claim must be dropped at 5h=97%%: %v", got)
	}

	// claim-free note: fresh -> shown; older than noteMaxAge -> aged out, while
	// a standing applyable fix of the same age survives.
	note := "CAUT|🔍 Bash dominates with 438 calls; consider /debug for the failing scenario"
	fix := "ADV|🔌 Audit Playwright MCP for the UI checks — https://github.com/microsoft/playwright-mcp"
	snap := cockpitSnapshot{Rate5hPct: 40}
	if got := parseSuggestionStore([]string{note, fix}, time.Minute, snap, cockpitState{}); len(got) != 2 {
		t.Fatalf("fresh note+fix: got %v", got)
	}
	got := parseSuggestionStore([]string{note, fix}, noteMaxAge+time.Minute, snap, cockpitState{})
	if len(got) != 1 || !strings.Contains(got[0].Text, "Playwright") {
		t.Fatalf("aged store must keep only the standing fix: %v", got)
	}
}

func TestAlertClassification(t *testing.T) {
	snap := cockpitSnapshot{ContextUsedPct: 92}
	c := classifySuggestion("📦 consider compact", snap, cockpitState{})
	if c.Level != AlertWarn {
		t.Fatalf("want WARN at 92%% ctx, got %s", c.Level)
	}
	if lvl, body, ok := parseSeverityPrefix("CAUT|🔍 use graphify"); !ok || lvl != AlertCaut || body == "" {
		t.Fatalf("parse prefix failed")
	}
}

func TestDetectPhase(t *testing.T) {
	if got := detectPhase(Signals{ContextUsedPct: 95}, ""); got != PhaseEmergency {
		t.Fatalf("want emergency, got %s", got)
	}
	if got := detectPhase(Signals{Turns: 3}, ""); got != PhasePreflight {
		t.Fatalf("want preflight, got %s", got)
	}
}

func TestRuleBasedSuggestions(t *testing.T) {
	sig := "context_used_pct=92  rate_5h_pct=10  searches=2"
	got := ruleBasedSuggestions(sig)
	if len(got) == 0 || got[0].Level != AlertWarn {
		t.Fatalf("want warn suggestion, got %+v", got)
	}
}

func TestChecklistSteps(t *testing.T) {
	if len(ChecklistSteps("context")) < 2 {
		t.Fatal("want checklist steps")
	}
}

func TestEnqueueAdvisorJob(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	sigPath := filepath.Join(dir, "s.signals")
	os.WriteFile(sigPath, []byte("turns=1"), 0o644)
	if err := enqueueAdvisorJob(sigPath, "sess", "/proj/x"); err != nil {
		t.Fatal(err)
	}
	jobs, _ := filepath.Glob(filepath.Join(jobDir(), "*.job"))
	if len(jobs) != 1 {
		t.Fatalf("want 1 job, got %d", len(jobs))
	}
	b, _ := os.ReadFile(jobs[0])
	var j advisorJob
	if json.Unmarshal(b, &j) != nil || j.Cwd != "/proj/x" {
		t.Fatalf("job must carry cwd for report stamping: %s", b)
	}
}

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("current process should be alive")
	}
	if processAlive(999999999) {
		t.Fatal("fake pid should not be alive")
	}
}

func TestAdvisorLinesWithPrefix(t *testing.T) {
	out := "WARN|⚠️ Context high — compact\nplain line\nMEMO|✅ efficient\n"
	got := advisorLines(out, 3)
	if len(got) != 2 {
		t.Fatalf("want 2 advisor lines, got %v", got)
	}
}

func TestParseApplyPlan(t *testing.T) {
	out := `Here is the plan:
{"summary":"use graphify","claude_md_section":"## Graphify\n- run graphify query","mcp_servers":{},"shell_commands":[]}`
	plan, err := parseApplyPlan(out)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary != "use graphify" || !strings.Contains(plan.ClaudeMDSection, "Graphify") {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestAppendClaudeMD(t *testing.T) {
	dir := t.TempDir()
	marker := suggestionMarker("🔍 use graphify")
	if err := appendClaudeMD(dir, "## Graphify\n- query first", marker); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if !strings.Contains(string(b), "Graphify") {
		t.Fatalf("missing section: %s", b)
	}
	// idempotent
	if err := appendClaudeMD(dir, "## Graphify\n- query first", marker); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if strings.Count(string(b2), "Graphify") != 1 {
		t.Fatalf("duplicate append: %s", b2)
	}
}

func TestAppendAgentInstructions(t *testing.T) {
	dir := t.TempDir()
	marker := suggestionMarker("🔍 use graphify")
	if err := appendAgentInstructions(dir, "## Graphify\n- query first", marker); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(dir, "CLAUDE.md"),
		filepath.Join(dir, "AGENTS.md"),
		filepath.Join(dir, ".cursor", "rules", "agent-flightdeck.mdc"),
	} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
		if !strings.Contains(string(b), "Graphify") {
			t.Fatalf("%s missing rule: %s", p, b)
		}
	}
	cursorRule, _ := os.ReadFile(filepath.Join(dir, ".cursor", "rules", "agent-flightdeck.mdc"))
	if !strings.HasPrefix(string(cursorRule), "---\n") || !strings.Contains(string(cursorRule), "alwaysApply: true") {
		t.Fatalf("Cursor rule should include frontmatter: %s", cursorRule)
	}
}

func TestMergeMCPServers(t *testing.T) {
	dir := t.TempDir()
	if err := mergeMCPServers(dir, map[string]any{
		"playwright": map[string]any{"command": "npx", "args": []any{"-y", "@playwright/mcp"}},
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if !strings.Contains(string(b), "playwright") {
		t.Fatalf("mcp not written: %s", b)
	}
}

func TestWriteSkillForClaudeAndCodex(t *testing.T) {
	dir := t.TempDir()
	if err := writeSkill(dir, "review", "# Review\n"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(dir, ".claude", "skills", "review", "SKILL.md"),
		filepath.Join(dir, ".codex", "skills", "review", "SKILL.md"),
	} {
		if b, err := os.ReadFile(p); err != nil || !strings.Contains(string(b), "Review") {
			t.Fatalf("skill not written to %s: %q err=%v", p, b, err)
		}
	}
}

func TestAnalyzeCadence(t *testing.T) {
	if got := analyzeCadence(1); got != 10 {
		t.Errorf("analyzeCadence(1)=%d want 10 (default)", got)
	}
	if got := analyzeCadence(12); got != 5 {
		t.Errorf("analyzeCadence(12)=%d want 5", got)
	}
	if got := analyzeCadence(30); got != 2 {
		t.Errorf("analyzeCadence(30)=%d want 2", got)
	}
	t.Setenv("COCKPIT_ANALYZE_CADENCE", "7")
	if got := analyzeCadence(1); got != 7 {
		t.Errorf("env override ignored: got %d want 7", got)
	}
}

// Regression: a short session (fewer than 10 turns) must still get advice —
// previously the advisor only fired on n%10==0, so it never fired at all.
func TestRunAnalyzeFiresEarlyInShortSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	// Pretend the advisor daemon is already running (our own pid, which is
	// alive) so RunAnalyze enqueues a job instead of spawning a real worker
	// that would shell out to `claude`.
	if err := os.WriteFile(daemonPIDFile(), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}

	tp := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(tp, []byte(`{"message":{"role":"user","content":"hello"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := stopInput{TranscriptPath: tp, SessionID: "short-session"}
	b, _ := json.Marshal(in)

	for i := 0; i < 3; i++ {
		RunAnalyze(bytes.NewReader(b))
	}
	jobs, _ := filepath.Glob(filepath.Join(jobDir(), "*.job"))
	if len(jobs) != 3 {
		t.Fatalf("want 3 queued jobs from the first 3 turns of a short session, got %d", len(jobs))
	}

	// Turn 4: past the early-fire window, default cadence k=10, 4%%10 != 0 -> throttled.
	RunAnalyze(bytes.NewReader(b))
	jobs, _ = filepath.Glob(filepath.Join(jobDir(), "*.job"))
	if len(jobs) != 3 {
		t.Fatalf("turn 4 should be throttled, got %d jobs", len(jobs))
	}
}

// Regression: a payload with a real context/cost snapshot but no rate_limits
// block (indistinguishable from a real 0%) must not erase previously known
// good 5h/7d values, and a momentarily-zero context window must not block
// persisting rate-limit data.
func TestWriteStatePersistsRateLimitsWithZeroContext(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	var sl slInput
	sl.RateLimits.FiveHour.UsedPercentage = 53
	sl.RateLimits.SevenDay.UsedPercentage = 9
	writeState(sl) // ContextWindowSize is 0 here

	st, ok := readState()
	if !ok || st.FiveH != 53 || st.SevenD != 9 {
		t.Fatalf("rate limits not persisted with zero context: ok=%v st=%+v", ok, st)
	}

	var sl2 slInput
	sl2.ContextWindow.ContextWindowSize = 200000
	sl2.ContextWindow.UsedPercentage = 40
	writeState(sl2) // no rate_limits block in this payload

	st, _ = readState()
	if st.FiveH != 53 || st.SevenD != 9 {
		t.Fatalf("rate limits clobbered by a payload without rate_limits: %+v", st)
	}
	if st.CtxSize != 200000 || st.CtxPct != 40 {
		t.Fatalf("context not updated: %+v", st)
	}
}

// Acceptance: state with 53/9 plus a payload without rate_limits must render
// "5h 53% · 7d 9%" instead of falling back to 0%.
func TestRenderStatuslineRateLimitFallback(t *testing.T) {
	var in slInput
	in.Workspace.CurrentDir = "/x/repo"
	st := cockpitState{FiveH: 53, SevenD: 9}
	rows := renderStatusline(in, nil, cockpitSnapshot{}, st)
	p := plain(rows[1])
	if !strings.Contains(p, "53%") || !strings.Contains(p, "9%") {
		t.Fatalf("want 5h 53%% . 7d 9%% fallback from state, got: %s", p)
	}
}

func TestRunCleanupRemovesOnlyThisSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	os.WriteFile(stateFile(), []byte(`{"five_h":53,"seven_d":9}`), 0o644)
	writeReportLines("clean-session", "/proj/a", []string{"ADV|🧹 x"})
	writeSnapshot("clean-session", cockpitSnapshot{Cwd: "/proj/a", Phase: "cruise"})
	writeReportLines("other-session", "/proj/b", []string{"ADV|🅱️ keep"})
	writeSnapshot("other-session", cockpitSnapshot{Cwd: "/proj/b", Phase: "cruise"})
	bumpCounter("clean-session")

	in := stopInput{SessionID: "clean-session"}
	b, _ := json.Marshal(in)
	RunCleanup(bytes.NewReader(b))

	if _, err := os.Stat(stateFile()); err != nil {
		t.Errorf("stateFile (account-wide rates) should survive SessionEnd: %v", err)
	}
	for _, p := range []string{sessionReportFile("clean-session"), sessionSnapshotFile("clean-session")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should be removed at SessionEnd", p)
		}
	}
	for _, p := range []string{sessionReportFile("other-session"), sessionSnapshotFile("other-session")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("another session's artifact must survive: %s: %v", p, err)
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(cockpitDir(), ".sa-count-*")); len(matches) != 0 {
		t.Errorf("session counter should be removed, got %v", matches)
	}
}

func TestCockpitDirConsolidatesState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	logsDir := filepath.Join(dir, "cockpit-logs")

	paths := map[string]string{
		"stateFile": stateFile(), "snapshotFile": sessionSnapshotFile("s"), "chimeFile": sessionChimeFile("s"),
		"reportFile": sessionReportFile("s"), "debugFile": debugFile(),
		"jobDir": jobDir(), "daemonPIDFile": daemonPIDFile(), "daemonLogFile": daemonLogFile(),
	}
	for name, got := range paths {
		if !strings.HasPrefix(got, logsDir) {
			t.Errorf("%s = %q, want under %q", name, got, logsDir)
		}
	}
	if _, err := os.Stat(logsDir); err != nil {
		t.Errorf("cockpitDir() should create the directory: %v", err)
	}
}

func TestRemoveSuggestion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if err := writeReportLines("s", "/proj/a", []string{"🔍 one", "💰 two", "🔄 three"}); err != nil {
		t.Fatal(err)
	}

	if err := removeSuggestion("s", 2); err != nil {
		t.Fatal(err)
	}
	got := readSuggestions("s")
	if len(got) != 2 || got[0] != "🔍 one" || got[1] != "🔄 three" {
		t.Fatalf("after remove: %v", got)
	}
	// the rewritten report must keep its session/cwd stamp for resolveSession.
	if st, ok := readSessionStamp(sessionReportFile("s")); !ok || st.Cwd != "/proj/a" || st.Session != "s" {
		t.Fatalf("stamp lost on rewrite: %+v ok=%v", st, ok)
	}
}
