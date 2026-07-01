// Command cockpit is a status line + session optimizer for Claude Code.
//
// Subcommands (wired into ~/.claude/settings.json):
//
//	cockpit statusline   # statusLine command — renders the two-row bar
//	cockpit analyze      # Stop hook — analyzes the session for token savings
//	cockpit install      # register statusLine + Stop hook in settings.json
//	cockpit uninstall    # remove cockpit settings and transient state
//	cockpit list         # show numbered suggestions
//	cockpit apply N      # accept suggestion N — updates CLAUDE.md, MCP, skills
//	cockpit worker FILE  # internal: detached background classifier (not for direct use)
//	cockpit version      # print version
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/Agent-Hellboy/claude-cockpit/internal/cockpit"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cockpit {statusline|analyze|install|uninstall|list|apply|worker|version}")
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
	case "worker":
		if len(os.Args) < 4 {
			os.Exit(0)
		}
		cockpit.RunWorker(os.Args[2], os.Args[3])
	case "cleanup":
		cockpit.RunCleanup(os.Stdin)
	case "install":
		if err := cockpit.Install(); err != nil {
			fmt.Fprintln(os.Stderr, "install failed:", err)
			os.Exit(1)
		}
	case "uninstall":
		if err := cockpit.Uninstall(); err != nil {
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
