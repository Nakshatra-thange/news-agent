package main

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"synergy/internal/config"
	"synergy/internal/enrich"
)

const summarizeUsage = `Usage:
  synergy summarize [--limit N] [--fake]   Summarize up to N items (default 3, max 20)

Selects the newest items without a current summary, so repeating the
command does not redo work. Items themselves are never modified.

Uses Claude when ANTHROPIC_API_KEY is set (model: ANTHROPIC_MODEL). --fake
uses the deterministic offline provider instead; its summaries are stored
with provider "fake" and are replaced once a real model summarizes the
same items.
`

// summarizeCmd runs one bounded, explicit summarization pass and prints one
// line per failure plus a summary.
func summarizeCmd(ctx context.Context, args []string, getenv func(string) string, stdout, logOut io.Writer) error {
	limit, fake := enrich.DefaultSummaryLimit, false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--fake":
			fake = true
		case a == "--limit" && i+1 < len(args):
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > enrich.MaxSummaryLimit {
				fmt.Fprintf(logOut, "--limit must be between 1 and %d\n\n%s", enrich.MaxSummaryLimit, summarizeUsage)
				return errUsage
			}
			limit = n
		case a == "-h" || a == "--help" || a == "help":
			fmt.Fprint(stdout, summarizeUsage)
			return nil
		default:
			fmt.Fprintf(logOut, "unknown argument %q\n\n%s", a, summarizeUsage)
			return errUsage
		}
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config:\n%w", err)
	}
	var provider enrich.Provider
	switch {
	case fake:
		provider = enrich.FakeProvider{}
	case cfg.LLM.AnthropicAPIKey != "":
		provider = enrich.NewAnthropicProvider(cfg.LLM.AnthropicAPIKey, cfg.LLM.AnthropicModel)
	default:
		fmt.Fprint(logOut, "no LLM provider is configured: set ANTHROPIC_API_KEY, or pass --fake for the offline fake provider\n\n"+summarizeUsage)
		return errUsage
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

	res, err := enrich.NewSummarizer(st, provider, logger).Run(ctx, limit)
	for _, f := range res.Failures {
		fmt.Fprintf(stdout, "failed  %s  %s: %v\n", f.Item.ID, truncateTitle(f.Item.Title), f.Err)
	}
	fmt.Fprintf(stdout, "provider %s (%s): selected %d, summarized %d, failed %d\n",
		provider.Name(), provider.Model(), res.Selected, res.Enriched, len(res.Failures))
	if err != nil {
		return err
	}
	if len(res.Failures) > 0 {
		return fmt.Errorf("%d of %d items could not be summarized", len(res.Failures), res.Selected)
	}
	return nil
}
