package main

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"synergy/internal/config"
	"synergy/internal/embed"
)

const embedUsage = `Usage:
  synergy embed [--limit N] [--fake]   Embed up to N items (default 10, max 50)

Selects the newest items without a current embedding for the model (none,
or one made from older item content), so repeating the command does not
redo work. Items themselves are never modified.

Uses Voyage AI when VOYAGE_API_KEY is set (model: VOYAGE_MODEL). --fake
uses the deterministic offline provider instead; its vectors are stored
with provider "fake" under their own model.
`

// embedCmd runs one bounded, explicit embedding pass and prints one line
// per failure plus a summary.
func embedCmd(ctx context.Context, args []string, getenv func(string) string, stdout, logOut io.Writer) error {
	limit, fake := embed.DefaultLimit, false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--fake":
			fake = true
		case a == "--limit" && i+1 < len(args):
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > embed.MaxLimit {
				fmt.Fprintf(logOut, "--limit must be between 1 and %d\n\n%s", embed.MaxLimit, embedUsage)
				return errUsage
			}
			limit = n
		case a == "-h" || a == "--help" || a == "help":
			fmt.Fprint(stdout, embedUsage)
			return nil
		default:
			fmt.Fprintf(logOut, "unknown argument %q\n\n%s", a, embedUsage)
			return errUsage
		}
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config:\n%w", err)
	}
	var provider embed.Provider
	switch {
	case fake:
		provider = embed.FakeProvider{}
	case cfg.Embedding.VoyageAPIKey != "":
		provider = embed.NewVoyageProvider(cfg.Embedding.VoyageAPIKey, cfg.Embedding.VoyageModel, "")
	default:
		fmt.Fprint(logOut, "no embedding provider is configured: set VOYAGE_API_KEY, or pass --fake for the offline fake provider\n\n"+embedUsage)
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

	res, err := embed.New(st, provider, logger).Run(ctx, limit)
	for _, f := range res.Failures {
		fmt.Fprintf(stdout, "failed  %s  %s: %v\n", f.Item.ID, truncateTitle(f.Item.Title), f.Err)
	}
	fmt.Fprintf(stdout, "provider %s (%s, %d dimensions): selected %d, embedded %d, failed %d\n",
		provider.Name(), provider.Model(), provider.Dimensions(), res.Selected, res.Embedded, len(res.Failures))
	if err != nil {
		return err
	}
	if len(res.Failures) > 0 {
		return fmt.Errorf("%d of %d items could not be embedded", len(res.Failures), res.Selected)
	}
	return nil
}
