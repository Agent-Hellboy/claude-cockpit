package cockpit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Logging & per-session artifacts live under ~/.claude/cockpit-logs/. They are
// kept for the life of the session (so you can inspect what the analyzer saw and
// suggested) and removed only when the session ends (see RunCleanup), not the
// moment a worker reads them.

func logDir() string { return filepath.Join(ConfigDir(), "cockpit-logs") }

var nonword = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

func safeSession(session string) string {
	s := nonword.ReplaceAllString(session, "_")
	if s == "" {
		s = "session"
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func sessionLogFile(session string) string {
	return filepath.Join(logDir(), safeSession(session)+".log")
}

func sessionSignalsFile(session string) string {
	return filepath.Join(logDir(), safeSession(session)+".signals")
}

// logf appends a timestamped line to the session's log file. Always on (this is
// the durable record the user asked for); failures are swallowed.
func logf(session, format string, args ...any) {
	if err := os.MkdirAll(logDir(), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(sessionLogFile(session), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}
