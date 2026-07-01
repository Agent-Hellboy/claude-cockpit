package cockpit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// advisorJob is queued for the persistent cockpit daemon (ECAM computer).
type advisorJob struct {
	Session     string `json:"session"`
	SignalsPath string `json:"signals_path"`
}

func daemonPIDFile() string { return filepath.Join(ConfigDir(), ".cockpit-daemon.pid") }
func daemonLogFile() string { return filepath.Join(ConfigDir(), ".cockpit-daemon.log") }
func jobDir() string        { return filepath.Join(ConfigDir(), "cockpit-jobs") }

func isDaemonRunning() bool {
	b, err := os.ReadFile(daemonPIDFile())
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return false
	}
	return processAlive(pid)
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

// RunDaemonStatus prints whether the advisor daemon is running.
func RunDaemonStatus(w io.Writer) {
	if isDaemonRunning() {
		b, _ := os.ReadFile(daemonPIDFile())
		fmt.Fprintf(w, "cockpit advisor daemon running (pid %s)\n", strings.TrimSpace(string(b)))
		pending, _ := filepath.Glob(filepath.Join(jobDir(), "*.job"))
		if len(pending) > 0 {
			fmt.Fprintf(w, "  queued jobs: %d\n", len(pending))
		}
		return
	}
	fmt.Fprintln(w, "cockpit advisor daemon not running")
	fmt.Fprintln(w, "  start with: cockpit daemon start")
}

// StartDaemonDetached launches the long-running advisor daemon in the background.
func StartDaemonDetached() error {
	if isDaemonRunning() {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "daemon", "run")
	cmd.Env = append(os.Environ(), "MODEL_HINT_GUARD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	if err := cmd.Start(); err != nil {
		return err
	}
	// wait briefly for pid file
	for i := 0; i < 20; i++ {
		if isDaemonRunning() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon started but pid file not ready")
}

// StopDaemon sends SIGTERM to the advisor daemon.
func StopDaemon() error {
	if !isDaemonRunning() {
		_ = os.Remove(daemonPIDFile())
		return nil
	}
	b, _ := os.ReadFile(daemonPIDFile())
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		for i := 0; i < 30; i++ {
			if !processAlive(pid) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	_ = os.Remove(daemonPIDFile())
	return nil
}

// RunDaemon runs the foreground advisor daemon (internal: cockpit daemon run).
func RunDaemon() {
	if os.Getenv("COCKPIT_ANALYZE_DISABLE") == "1" {
		fmt.Fprintln(os.Stderr, "daemon: COCKPIT_ANALYZE_DISABLE is set")
		os.Exit(1)
	}
	if err := os.MkdirAll(ConfigDir(), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "daemon:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(jobDir(), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "daemon:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(daemonPIDFile(), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "daemon:", err)
		os.Exit(1)
	}
	defer os.Remove(daemonPIDFile())

	daemonLog("daemon: start pid=%d (acquisition + advisor LRU)", os.Getpid())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go acquisitionLoop(ctx)
	go advisorLoop(ctx)

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	daemonLog("daemon: shutdown")
	cancel()
	time.Sleep(200 * time.Millisecond)
}

func daemonLog(format string, args ...any) {
	msg := fmt.Sprintf("%s "+format+"\n", append([]any{time.Now().Format(time.RFC3339)}, args...)...)
	_ = os.MkdirAll(ConfigDir(), 0o755)
	f, err := os.OpenFile(daemonLogFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(msg)
}

// acquisitionLoop is the data-acquisition LRU — keeps instrument snapshot fresh.
func acquisitionLoop(ctx context.Context) {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			st, ok := readState()
			if !ok {
				continue
			}
			snap := readSnapshot()
			snap.ContextUsedPct = st.CtxPct
			snap.Rate5hPct = st.FiveH
			writeSnapshot(snap)
		}
	}
}

// advisorLoop is the ECAM alerting LRU — processes queued advisor jobs one at a time.
func advisorLoop(ctx context.Context) {
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			job, path, ok := nextJob()
			if !ok {
				continue
			}
			daemonLog("daemon: advisor job session=%s", job.Session)
			RunWorker(job.SignalsPath, job.Session)
			_ = os.Remove(path)
		}
	}
}

func nextJob() (advisorJob, string, bool) {
	entries, err := os.ReadDir(jobDir())
	if err != nil {
		return advisorJob{}, "", false
	}
	var jobs []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".job") {
			jobs = append(jobs, e.Name())
		}
	}
	if len(jobs) == 0 {
		return advisorJob{}, "", false
	}
	sort.Strings(jobs)
	path := filepath.Join(jobDir(), jobs[0])
	b, err := os.ReadFile(path)
	if err != nil {
		_ = os.Remove(path)
		return advisorJob{}, "", false
	}
	var j advisorJob
	if json.Unmarshal(b, &j) != nil || j.Session == "" || j.SignalsPath == "" {
		_ = os.Remove(path)
		return advisorJob{}, "", false
	}
	return j, path, true
}

func enqueueAdvisorJob(signalsPath, session string) error {
	if err := os.MkdirAll(jobDir(), 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%d.job", sessionKey(session), time.Now().UnixNano())
	path := filepath.Join(jobDir(), name)
	b, err := json.Marshal(advisorJob{Session: session, SignalsPath: signalsPath})
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// dispatchAdvisor sends work to the daemon queue, or spawns a one-shot worker.
func dispatchAdvisor(signals, session string) {
	if err := os.MkdirAll(logDir(), 0o755); err != nil {
		logf(session, "dispatchAdvisor: mkdir logs: %v", err)
		return
	}
	sigPath := sessionSignalsFile(session)
	if err := os.WriteFile(sigPath, []byte(signals), 0o644); err != nil {
		logf(session, "dispatchAdvisor: write signals: %v", err)
		return
	}
	if isDaemonRunning() {
		if err := enqueueAdvisorJob(sigPath, session); err != nil {
			logf(session, "dispatchAdvisor: enqueue failed: %v — fallback worker", err)
			spawnOneShotWorker(sigPath, session)
			return
		}
		logf(session, "dispatchAdvisor: queued for daemon")
		return
	}
	spawnOneShotWorker(sigPath, session)
}

func spawnOneShotWorker(sigPath, session string) {
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
