// Command cockpit is a status line + session optimizer for coding agents.
//
// Subcommands (wired into Claude Code's ~/.claude/settings.json where hooks exist):
//
//	cockpit statusline   # statusLine command — renders the two-row bar
//	cockpit analyze      # Stop hook — analyzes the session for token savings
//	cockpit install      # register Claude Code hooks, or install codex/cursor/all
//	cockpit uninstall    # remove cockpit settings, or uninstall codex/cursor/all
//	cockpit list         # show numbered suggestions
//	cockpit apply N      # accept suggestion N — updates agent rules, MCP, skills
//	cockpit systems      # ECAM synoptic of hooks, MCP, skills, graphify
//	cockpit checklist T  # ECAM procedure for a topic (context, budget, search)
//	cockpit plan         # FMS-style session route and deviation
//	cockpit status       # ECAM STATUS deferred items
//	cockpit debrief      # post-session black-box summary
//	cockpit memory       # retrieve compact background session memory
//	cockpit daemon       # advisor daemon status
//	cockpit daemon start # start persistent ECAM advisor LRU
//	cockpit daemon stop  # stop advisor daemon
//	cockpit daemon run   # internal foreground daemon (not for direct use)
//	cockpit worker FILE  # internal: one-shot advisor (not for direct use)
//	cockpit version      # print version
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Agent-Hellboy/agent-flightdeck/internal/cockpit"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cockpit {statusline|analyze|install [claude|codex|cursor|all]|uninstall [claude|codex|cursor|all]|list|apply|systems|checklist|plan|status|debrief|memory|daemon|worker|version}")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "statusline":
		cockpit.RunStatusline(os.Stdin, os.Stdout)
	case "analyze":
		cockpit.RunAnalyze(os.Stdin)
	case "list":
		cockpit.RunList(os.Stdout)
	case "apply":
		fs := flag.NewFlagSet("apply", flag.ExitOnError)
		yes := fs.Bool("yes", false, "apply without confirmation prompt")
		dryRun := fs.Bool("dry-run", false, "show plan only")
		cwd := fs.String("cwd", "", "project directory (default: current)")
		_ = fs.Parse(os.Args[2:])
		args := fs.Args()
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "usage: cockpit apply <n> [--yes] [--dry-run] [--cwd DIR]")
			os.Exit(2)
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 {
			fmt.Fprintln(os.Stderr, "apply: suggestion number must be a positive integer")
			os.Exit(2)
		}
		if err := cockpit.RunApply(n, *cwd, *yes, *dryRun); err != nil {
			fmt.Fprintln(os.Stderr, "apply failed:", err)
			os.Exit(1)
		}
	case "systems":
		cwd, _ := os.Getwd()
		if len(os.Args) > 2 {
			cwd = os.Args[2]
		}
		cockpit.RunSystems(os.Stdout, cwd)
	case "checklist":
		topic := "general"
		if len(os.Args) > 2 {
			topic = os.Args[2]
		}
		cockpit.RunChecklist(os.Stdout, topic)
	case "plan":
		cockpit.RunPlan(os.Stdout)
	case "status":
		cwd, _ := os.Getwd()
		if len(os.Args) > 2 {
			cwd = os.Args[2]
		}
		cockpit.RunStatus(os.Stdout, cwd)
	case "debrief":
		session := ""
		if len(os.Args) > 2 {
			session = os.Args[2]
		}
		cockpit.RunDebrief(os.Stdout, session)
	case "memory":
		fs := flag.NewFlagSet("memory", flag.ExitOnError)
		jsonOut := fs.Bool("json", false, "print JSONL for machine consumption")
		limit := fs.Int("limit", 20, "maximum entries to print")
		scan := fs.Bool("scan", false, "scan sessions before reading memory")
		_ = fs.Parse(os.Args[2:])
		if *scan {
			if err := cockpit.RunMemoryScan(); err != nil {
				fmt.Fprintln(os.Stderr, "memory scan failed:", err)
				os.Exit(1)
			}
		}
		cockpit.RunMemory(os.Stdout, strings.Join(fs.Args(), " "), *limit, *jsonOut)
	case "daemon":
		sub := "status"
		if len(os.Args) > 2 {
			sub = os.Args[2]
		}
		switch sub {
		case "start":
			if err := cockpit.StartDaemonDetached(); err != nil {
				fmt.Fprintln(os.Stderr, "daemon start:", err)
				os.Exit(1)
			}
			cockpit.RunDaemonStatus(os.Stdout)
		case "stop":
			if err := cockpit.StopDaemon(); err != nil {
				fmt.Fprintln(os.Stderr, "daemon stop:", err)
				os.Exit(1)
			}
			fmt.Println("Advisor daemon stopped.")
		case "run":
			cockpit.RunDaemon()
		case "status", "":
			cockpit.RunDaemonStatus(os.Stdout)
		default:
			fmt.Fprintln(os.Stderr, "usage: cockpit daemon {start|stop|status}")
			os.Exit(2)
		}
	case "worker":
		if len(os.Args) < 4 {
			os.Exit(0)
		}
		cwd := ""
		if len(os.Args) > 4 {
			cwd = os.Args[4]
		}
		cockpit.RunWorker(os.Args[2], os.Args[3], cwd)
	case "cleanup":
		cockpit.RunCleanup(os.Stdin)
	case "install":
		if err := cockpit.Install(os.Args[2:]...); err != nil {
			fmt.Fprintln(os.Stderr, "install failed:", err)
			os.Exit(1)
		}
	case "uninstall":
		if err := cockpit.Uninstall(os.Args[2:]...); err != nil {
			fmt.Fprintln(os.Stderr, "uninstall failed:", err)
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Println("cockpit", version)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}
