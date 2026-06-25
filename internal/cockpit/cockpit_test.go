package cockpit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if got := gauge(100); got != strings.Repeat("▓", 10) {
		t.Errorf("gauge(100)=%q", got)
	}
	if got := gauge(150); got != strings.Repeat("▓", 10) {
		t.Errorf("gauge over 100 should clamp: %q", got)
	}
	if got := gauge(50); got != strings.Repeat("▓", 5)+strings.Repeat("░", 5) {
		t.Errorf("gauge(50)=%q", got)
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
	var in slInput
	in.Model.DisplayName = "Opus 4.8 (1M context)"
	in.Effort.Level = "high"
	in.Workspace.CurrentDir = "/x/mcp-runtime"
	in.Worktree.Branch = "main"
	in.ContextWindow.UsedPercentage = 99
	in.ContextWindow.TotalInputTokens = 985000
	in.ContextWindow.ContextWindowSize = 1000000
	in.Cost.TotalCostUSD = 24.3
	rows := renderStatusline(in, "")
	if len(rows) != 2 {
		t.Fatalf("want 2 rows (no hint), got %d", len(rows))
	}
	p := plain(rows[0])
	for _, want := range []string{"mcp-runtime", "⎇main", "Opus 4.8 (1M context)", "high", "ctx", "99%", "985k/1.0M", "⚠ /compact"} {
		if !strings.Contains(p, want) {
			t.Errorf("row1 missing %q: %s", want, p)
		}
	}
}

func TestRenderStatuslinePRAndHint(t *testing.T) {
	var in slInput
	in.Workspace.CurrentDir = "/x/repo"
	in.Worktree.Branch = "feat"
	in.PR.Number = json.Number("336")
	in.PR.ReviewState = "APPROVED"
	rows := renderStatusline(in, "💡 use sonnet")
	if len(rows) != 3 {
		t.Fatalf("want 3 rows (with hint), got %d", len(rows))
	}
	if !strings.Contains(plain(rows[0]), "⇡#336") {
		t.Errorf("PR segment missing: %s", plain(rows[0]))
	}
	if plain(rows[2]) != "💡 use sonnet" {
		t.Errorf("hint row wrong: %q", plain(rows[2]))
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
	setStopHook(m, cmd)
	setStopHook(m, cmd) // twice -> must not duplicate

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
	removeStopHook(m, cmd)
	delete(m, "statusLine") // simulate Uninstall's statusLine drop
	stop = toList(m["hooks"].(map[string]any)["Stop"])
	if len(stop) != 1 {
		t.Fatalf("want 1 remaining (foreign) group, got %d", len(stop))
	}
	if m["model"] != "opus" {
		t.Error("uninstall dropped unrelated key")
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

	in := stopInput{TranscriptPath: tp, Cwd: dir, SessionID: "s"}
	sig := gatherSignals(in, 30)
	for _, want := range []string{"searches=5", "files_reread_3x+=1", "graphify_graph=no", "model=claude-opus-4-8", "Bash:5", "Read:3", "rename this field"} {
		if !strings.Contains(sig, want) {
			t.Errorf("signals missing %q:\n%s", want, sig)
		}
	}

	// cadence: counter is independent state per session id.
	if got := bumpCounter("c1"); got != 1 {
		t.Errorf("first bump=%d want 1", got)
	}
	if got := bumpCounter("c1"); got != 2 {
		t.Errorf("second bump=%d want 2", got)
	}
}
