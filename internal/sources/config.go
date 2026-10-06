package sources

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"synergy/internal/domain"
)

// DecodeConfig strictly decodes a source configuration object into dst.
// Empty input is treated as {} so every field takes its default. Unknown
// fields, wrong types and trailing data are reported as validation errors.
func DecodeConfig(raw json.RawMessage, dst any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		raw = json.RawMessage(`{}`)
	} else if trimmed[0] != '{' {
		// Decoding null into a struct is a silent no-op; reject it (and any
		// other non-object) explicitly.
		return configError("must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return configError(describeDecodeError(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return configError("must contain a single JSON object")
	}
	return nil
}

// EncodeConfig marshals a normalized configuration.
func EncodeConfig(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return b, nil
}

func configError(msg string) error {
	return &domain.ValidationError{Fields: []domain.FieldError{{Field: "config", Message: msg}}}
}

func describeDecodeError(err error) string {
	var (
		typeErr   *json.UnmarshalTypeError
		syntaxErr *json.SyntaxError
	)
	switch {
	case errors.As(err, &typeErr) && typeErr.Field == "":
		return "must be a JSON object"
	case errors.As(err, &typeErr):
		return fmt.Sprintf("field %q must be %s", typeErr.Field, typeErr.Type)
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
		return "must be valid JSON"
	default:
		// Unknown fields and errors from custom decoders (e.g. durations).
		return strings.TrimPrefix(err.Error(), "json: ")
	}
}

// Checks accumulates configuration validation errors, prefixing field names
// with "config.".
type Checks struct {
	fields []domain.FieldError
}

// Check records an error for field unless ok.
func (c *Checks) Check(ok bool, field, format string, args ...any) {
	if !ok {
		c.fields = append(c.fields, domain.FieldError{Field: "config." + field, Message: fmt.Sprintf(format, args...)})
	}
}

// Err returns a *domain.ValidationError, or nil if every check passed.
func (c *Checks) Err() error {
	if len(c.fields) == 0 {
		return nil
	}
	return &domain.ValidationError{Fields: c.fields}
}

// Duration is a time.Duration that reads and writes JSON as a string such as
// "90m", "48h" or "7d" (whole days, a convenience Go's parser lacks).
type Duration time.Duration

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// MarshalJSON writes the duration in its shortest readable form.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(formatDuration(time.Duration(d)))
}

// UnmarshalJSON accepts Go duration strings plus an "Nd" days form.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New(`duration must be a string such as "48h" or "7d"`)
	}
	v, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// ParseDuration parses Go duration syntax or a whole number of days ("7d").
func ParseDuration(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return v, nil
}

func formatDuration(d time.Duration) string {
	switch {
	case d == 0:
		return "0s"
	case d%(24*time.Hour) == 0:
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	default:
		return d.String()
	}
}

// CleanStrings trims each string and drops empties and exact duplicates,
// keeping first-seen order.
func CleanStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
