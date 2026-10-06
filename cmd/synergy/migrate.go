package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"text/tabwriter"

	"synergy/internal/config"
)

const migrateUsage = `Usage:
  synergy migrate [up]        Apply all pending migrations (default)
  synergy migrate status      List migrations and whether each is applied
  synergy migrate version     Print the current and latest schema versions
  synergy migrate down --yes  Roll back the most recent migration (destroys data)
`

// migrate manages the database schema using the migrations embedded in the
// binary. Human-readable results go to stdout; structured logs to logOut.
func migrate(ctx context.Context, args []string, getenv func(string) string, stdout, logOut io.Writer) error {
	sub := "up"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "up", "status", "version":
	case "down":
		if !slices.Contains(args[1:], "--yes") {
			fmt.Fprint(logOut, "migrate down drops schema objects and their data; re-run with --yes to confirm\n\n"+migrateUsage)
			return errUsage
		}
	case "help", "-h", "--help":
		fmt.Fprint(stdout, migrateUsage)
		return nil
	default:
		fmt.Fprintf(logOut, "unknown migrate command %q\n\n%s", sub, migrateUsage)
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

	switch sub {
	case "up":
		results, err := st.MigrateUp(ctx)
		for _, r := range results {
			fmt.Fprintf(stdout, "applied   %s (%s)\n", r.Name, r.Duration.Round(1e6))
		}
		if err != nil {
			logger.Error("migration failed", targetAttrs(st), "err", err)
			return err
		}
		current, _, err := st.SchemaVersions(ctx)
		if err != nil {
			return err
		}
		if len(results) == 0 {
			fmt.Fprintf(stdout, "database is up to date (schema version %d)\n", current)
		} else {
			fmt.Fprintf(stdout, "schema version is now %d\n", current)
		}
		logger.Info("migrations complete", targetAttrs(st), "applied", len(results), "schema_version", current)

	case "down":
		r, err := st.MigrateDown(ctx)
		if err != nil {
			logger.Error("rollback failed", targetAttrs(st), "err", err)
			return err
		}
		fmt.Fprintf(stdout, "rolled back %s (%s)\n", r.Name, r.Duration.Round(1e6))
		logger.Warn("migration rolled back", targetAttrs(st), "version", r.Version, "name", r.Name)

	case "status":
		statuses, err := st.MigrationStatuses(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "VERSION\tNAME\tSTATE\tAPPLIED AT")
		for _, m := range statuses {
			state, at := "pending", "-"
			if m.Applied {
				state, at = "applied", m.AppliedAt.UTC().Format("2006-01-02 15:04:05Z")
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", m.Version, m.Name, state, at)
		}
		return tw.Flush()

	case "version":
		current, latest, err := st.SchemaVersions(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "current %d\nlatest  %d\n", current, latest)
	}
	return nil
}
