package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/seppaleinen/idle-deck/config"
)

const version = "0.0.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "--version", "-version", "version":
		fmt.Printf("idle-deck %s\n", version)
		return
	case "check":
		os.Exit(runCheck(os.Args[2:]))
	case "run":
		os.Exit(runRun(os.Args[2:]))
	case "status":
		os.Exit(runStatus(os.Args[2:]))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: idle-deck <command> [flags]")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  check   validate configuration and connectivity")
	fmt.Fprintln(os.Stderr, "  run     run the daemon in the foreground")
	fmt.Fprintln(os.Stderr, "  status  report what the daemon is doing")
	fmt.Fprintln(os.Stderr, "  --version  print the version")
}

func runCheck(args []string) int {
	fs := flag.NewFlagSet("idle-deck check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	flags := config.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Step 4: SQLite path writable; schema-current is deferred (no storage schema exists yet).
	if err := config.CheckDB(cfg.DB); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Step 6: resolved configuration with secrets redacted.
	fmt.Print(config.Format(config.Redact(cfg)))
	return 0
}

func runRun(args []string) int {
	fs := flag.NewFlagSet("idle-deck run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	flags := config.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if _, err := config.Load(flags); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
    }
	fmt.Println("daemon not implemented (issue #10)")
    return 1
}

func runStatus(args []string) int {
    // status is a stub; it does not require config validation (issue #11).
    fmt.Println("status: no state yet (issue #10)")
    return 0
}