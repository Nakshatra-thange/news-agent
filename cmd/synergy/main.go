// Command synergy is the single entry point for the Synergy service.
//
// Subcommands:
//
//	serve     run the HTTP API
//	migrate   manage the database schema
//	seed      register the default sources
//	fetch     fetch sources now (synchronously)
//	version   print the build version
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `Synergy: personalized AI-development intelligence.

Usage:
  synergy <command>

Commands:
  serve     Run the HTTP API server
  migrate   Manage the database schema (up, status, version, down)
  seed      Register the default sources (idempotent)
  fetch     Fetch sources now: fetch <slug>... | fetch --all [--force]
  version   Print the build version
  help      Show this help

Configuration is read from environment variables; see .env.example.
`

var errUsage = errors.New("invalid usage")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	if err != nil {
		if !errors.Is(err, errUsage) {
			fmt.Fprintln(os.Stderr, "synergy:", err)
		}
		os.Exit(1)
	}
}

// run dispatches a subcommand. Command output goes to stdout; logs and
// diagnostics go to stderr.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errUsage
	}
	switch args[0] {
	case "serve":
		return serve(ctx, getenv, stderr)
	case "migrate":
		return migrate(ctx, args[1:], getenv, stdout, stderr)
	case "seed":
		return seed(ctx, getenv, stdout, stderr)
	case "fetch":
		return fetch(ctx, args[1:], getenv, stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, version)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return errUsage
	}
}
