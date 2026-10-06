package sources

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"synergy/internal/domain"
)

func TestDuration(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
		out  string
	}{
		{`"48h"`, 48 * time.Hour, `"2d"`},
		{`"7d"`, 7 * 24 * time.Hour, `"7d"`},
		{`"90m"`, 90 * time.Minute, `"90m"`},
		{`"1h30m"`, 90 * time.Minute, `"90m"`},
		{`"5h"`, 5 * time.Hour, `"5h"`},
		{`"45s"`, 45 * time.Second, `"45s"`},
	}
	for _, tt := range tests {
		var d Duration
		if err := json.Unmarshal([]byte(tt.in), &d); err != nil {
			t.Errorf("Unmarshal(%s): %v", tt.in, err)
			continue
		}
		if d.D() != tt.want {
			t.Errorf("Unmarshal(%s) = %v, want %v", tt.in, d.D(), tt.want)
		}
		out, _ := json.Marshal(d)
		if string(out) != tt.out {
			t.Errorf("Marshal(%v) = %s, want %s", tt.want, out, tt.out)
		}
	}
	for _, bad := range []string{`"7 days"`, `"-1d"`, `"xd"`, `48`, `null`} {
		var d Duration
		if err := json.Unmarshal([]byte(bad), &d); err == nil && bad != "null" {
			t.Errorf("Unmarshal(%s) succeeded, want error", bad)
		}
	}
}

func TestDecodeConfigErrors(t *testing.T) {
	type cfg struct {
		N int `json:"n"`
	}
	tests := []struct {
		raw     string
		wantMsg string
	}{
		{`{"n": "x"}`, `field "n" must be int`},
		{`{"m": 1}`, `unknown field "m"`},
		{`[1]`, "must be a JSON object"},
		{`null`, "must be a JSON object"},
		{`{"n": 1`, "must be valid JSON"},
		{`{"n": 1} {"n": 2}`, "must contain a single JSON object"},
	}
	for _, tt := range tests {
		var c cfg
		err := DecodeConfig(json.RawMessage(tt.raw), &c)
		var ve *domain.ValidationError
		if !errors.As(err, &ve) || len(ve.Fields) != 1 || ve.Fields[0].Field != "config" {
			t.Errorf("DecodeConfig(%s) = %v, want a config ValidationError", tt.raw, err)
			continue
		}
		if ve.Fields[0].Message != tt.wantMsg {
			t.Errorf("DecodeConfig(%s) message = %q, want %q", tt.raw, ve.Fields[0].Message, tt.wantMsg)
		}
	}
}

func TestCleanStrings(t *testing.T) {
	got := CleanStrings([]string{" a ", "", "b", "a", "  "})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("CleanStrings = %q", got)
	}
}
