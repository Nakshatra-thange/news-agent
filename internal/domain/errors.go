// Package domain defines Synergy's core types (sources, items, fetch runs),
// their validation rules and the sentinel errors shared across layers. It has
// no I/O and no dependencies on other Synergy packages.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel errors. Lower layers wrap these so upper layers (for example the
// HTTP API) can map them to responses with errors.Is.
var (
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("conflict")
	ErrInvalid         = errors.New("invalid")
	ErrFetchInProgress = errors.New("a fetch is already running for this source")
	// ErrUnsupported means the operation is not available, e.g. no fetch
	// adapter is registered for a source type yet.
	ErrUnsupported = errors.New("not supported")
	// ErrTooSoon means a rate or politeness limit forbids the operation now.
	ErrTooSoon = errors.New("too soon")
	// ErrUnavailable means a dependency (the database) cannot be reached;
	// the same request may succeed later.
	ErrUnavailable = errors.New("unavailable")
)

// CooldownError reports that a source was fetched too recently. It matches
// ErrTooSoon with errors.Is.
type CooldownError struct {
	Slug      string
	Remaining time.Duration
}

func (e *CooldownError) Error() string {
	return fmt.Sprintf("source %q was fetched recently; next fetch allowed in %s", e.Slug, e.Remaining.Round(time.Second))
}

func (e *CooldownError) Unwrap() error { return ErrTooSoon }

// FieldError describes one invalid field.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationError collects every invalid field so callers can report them all
// at once. It matches ErrInvalid with errors.Is.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Field + ": " + f.Message
	}
	return "invalid: " + strings.Join(parts, "; ")
}

func (e *ValidationError) Unwrap() error { return ErrInvalid }

// validator accumulates field errors.
type validator struct {
	fields []FieldError
}

func (v *validator) check(ok bool, field, message string) {
	if !ok {
		v.fields = append(v.fields, FieldError{Field: field, Message: message})
	}
}

func (v *validator) err() error {
	if len(v.fields) == 0 {
		return nil
	}
	return &ValidationError{Fields: v.fields}
}
