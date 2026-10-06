// Package migrations embeds Synergy's versioned SQL migrations into the binary.
//
// Files are named NNNNN_description.sql and use goose annotations
// (-- +goose Up / -- +goose Down). Never edit a migration that has been
// applied anywhere; add a new one instead.
package migrations

import "embed"

// FS holds every *.sql migration in this directory.
//
//go:embed *.sql
var FS embed.FS
