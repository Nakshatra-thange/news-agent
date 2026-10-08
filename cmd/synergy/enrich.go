package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"synergy/internal/config"
	"synergy/internal/enrich"
)

const enrichUsage = `Usage:
  synergy enrich --fake [--limit N]   Enrich up to N items (default 10, max 100)

Selects the newest items without a current enrichment, so repeating the
command does not redo work. Items themselves are never modified.

No real LLM provider is integrated yet. --fake uses the deterministic,
offline fake provider; its results are stored with provider "fake" and are
replaced once a real model enriches the same items.
`

// enrichCmd runs one bounded, explicit enrichment pass and prints one line
// per failure plus a summary.
func enrichCmd(ctx context.Context, args []string, getenv func(string) string, stdout, logOut io.Writer) error {
	limit, fake := enrich.DefaultLimit, false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--fake":
			fake = true
		case a == "--limit" && i+1 < len(args):
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > enrich.MaxLimit {
				fmt.Fprintf(logOut, "--limit must be between 1 and %d\n\n%s", enrich.MaxLimit, enrichUsage)
				return errUsage
			}
			limit = n
		case a == "-h" || a == "--help" || a == "help":
			fmt.Fprint(stdout, enrichUsage)
			return nil
		default:
			fmt.Fprintf(logOut, "unknown argument %q\n\n%s", a, enrichUsage)
			return errUsage
		}
	}
	if !fake {
		fmt.Fprint(logOut, "no LLM provider is configured yet; pass --fake to use the offline fake provider\n\n"+enrichUsage)
		return errUsage
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config:\n%w", err)
	}
	logger := newLogger(logOut, cfg.Log)
	st, err := openStore(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := pingDB(ctx, st, cfg.Database); err != nil {
		logger.Error("database unreachable", targetAttrs(st), "err", err)
		return err
	}

	res, err := enrich.New(st, enrich.FakeProvider{}, logger).Run(ctx, limit)
	for _, f := range res.Failures {
		fmt.Fprintf(stdout, "failed  %s  %s: %v\n", f.Item.ID, truncateTitle(f.Item.Title), f.Err)
	}
	fmt.Fprintf(stdout, "selected %d, enriched %d, failed %d\n", res.Selected, res.Enriched, len(res.Failures))
	if err != nil {
		return err
	}
	if len(res.Failures) > 0 {
		return fmt.Errorf("%d of %d items could not be enriched", len(res.Failures), res.Selected)
	}
	return nil
}

func truncateTitle(s string) string {
	if r := []rune(s); len(r) > 60 {
		return strings.TrimSpace(string(r[:60])) + "..."
	}
	return s
}
