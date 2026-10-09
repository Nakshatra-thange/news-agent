package main

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"synergy/internal/cluster"
	"synergy/internal/config"
	"synergy/internal/embed"
)

var clusterUsage = fmt.Sprintf(`Usage:
  synergy cluster [--limit N] [--threshold T] [--fake]
      Group up to N items (default %d, max %d) into stories

Takes the oldest items that have a current embedding (see synergy embed)
and no story yet. Each joins the story whose first item is most similar
(cosine similarity at least T, default %.2f), or starts a new story.
Repeating the command does not redo work. Items are never modified, and
no API is called.

Uses the embeddings of VOYAGE_MODEL; --fake uses those of the offline fake
provider instead. Different models are never mixed.
`, cluster.DefaultLimit, cluster.MaxLimit, cluster.DefaultThreshold)

// clusterCmd runs one bounded, explicit clustering pass and prints one line
// per failure plus a summary.
func clusterCmd(ctx context.Context, args []string, getenv func(string) string, stdout, logOut io.Writer) error {
	limit, threshold, fake := cluster.DefaultLimit, cluster.DefaultThreshold, false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--fake":
			fake = true
		case a == "--limit" && i+1 < len(args):
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > cluster.MaxLimit {
				fmt.Fprintf(logOut, "--limit must be between 1 and %d\n\n%s", cluster.MaxLimit, clusterUsage)
				return errUsage
			}
			limit = n
		case a == "--threshold" && i+1 < len(args):
			i++
			t, err := strconv.ParseFloat(args[i], 64)
			if err != nil || !(t > 0 && t <= 1) {
				fmt.Fprintf(logOut, "--threshold must be greater than 0 and at most 1\n\n%s", clusterUsage)
				return errUsage
			}
			threshold = t
		case a == "-h" || a == "--help" || a == "help":
			fmt.Fprint(stdout, clusterUsage)
			return nil
		default:
			fmt.Fprintf(logOut, "unknown argument %q\n\n%s", a, clusterUsage)
			return errUsage
		}
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config:\n%w", err)
	}
	opts := cluster.Options{Model: cfg.Embedding.VoyageModel, Dimensions: embed.VoyageDimensions, Threshold: threshold}
	if fake {
		p := embed.FakeProvider{}
		opts.Model, opts.Dimensions = p.Model(), p.Dimensions()
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

	res, err := cluster.New(st, opts, logger).Run(ctx, limit)
	for _, f := range res.Failures {
		fmt.Fprintf(stdout, "failed  %s: %v\n", f.ItemID, f.Err)
	}
	fmt.Fprintf(stdout, "model %s (%d dimensions, threshold %.2f): selected %d, joined %d, new stories %d, failed %d\n",
		opts.Model, opts.Dimensions, opts.Threshold, res.Selected, res.Joined, res.Created, len(res.Failures))
	if err != nil {
		return err
	}
	if len(res.Failures) > 0 {
		return fmt.Errorf("%d of %d items could not be clustered", len(res.Failures), res.Selected)
	}
	return nil
}
