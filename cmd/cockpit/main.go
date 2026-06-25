// Command cockpit is a status line + session optimizer for Claude Code.
//
// Subcommands (wired into ~/.claude/settings.json):
//
//	cockpit statusline   # statusLine command — renders the two-row bar
//	cockpit analyze      # Stop hook — analyzes the session for token savings
//	cockpit worker FILE  # internal: detached background classifier (not for direct use)
//	cockpit version      # print version
package main

import (
	"fmt"
	"os"

	"github.com/Agent-Hellboy/claude-cockpit/internal/cockpit"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cockpit {statusline|analyze|version}")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "statusline":
		cockpit.RunStatusline(os.Stdin, os.Stdout)
	case "analyze":
		cockpit.RunAnalyze(os.Stdin)
	case "worker":
		if len(os.Args) < 3 {
			os.Exit(0)
		}
		cockpit.RunWorker(os.Args[2])
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
