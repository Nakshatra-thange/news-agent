package domain

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func validSummary() Summary {
	h := sha256.Sum256([]byte("content"))
	return Summary{
		ItemID:        uuid.New(),
		Text:          "A paper that measures how tool-using agents scale with model size and test-time compute.",
		Provider:      "fake",
		Model:         "fake-1",
		PromptVersion: 1,
		ContentHash:   h[:],
	}
}

func TestSummaryNormalize(t *testing.T) {
	s := validSummary()
	s.Text = "  A paper that\nmeasures how agents\n\nscale.   It matters.  "
	s.Normalize()
	if s.Text != "A paper that measures how agents scale. It matters." {
		t.Errorf("normalized = %q", s.Text)
	}
}

func TestSummaryValidate(t *testing.T) {
	if err := validSummary().Validate(); err != nil {
		t.Fatalf("valid summary rejected: %v", err)
	}
	tests := []struct {
		name  string
		edit  func(*Summary)
		field string
	}{
		{"no item", func(s *Summary) { s.ItemID = uuid.Nil }, "item_id"},
		{"empty", func(s *Summary) { s.Text = "" }, "summary"},
		{"too short", func(s *Summary) { s.Text = "Too short." }, "summary"},
		{"too long", func(s *Summary) { s.Text = strings.Repeat("word ", MaxSummaryLen/5+1) }, "summary"},
		{"multi paragraph", func(s *Summary) { s.Text = validSummary().Text + "\nSecond paragraph here." }, "summary"},
		{"code fence", func(s *Summary) { s.Text = "```text\nA summary that is long enough to pass.\n```" }, "summary"},
		{"json", func(s *Summary) { s.Text = `{"summary": "A summary that is long enough to pass."}` }, "summary"},
		{"heading", func(s *Summary) { s.Text = "# Summary of a paper about tool-using agents" }, "summary"},
		{"bullets", func(s *Summary) { s.Text = "- A paper about agents that is long enough to pass" }, "summary"},
		{"invalid utf8", func(s *Summary) { s.Text = "A summary that is long enough to pass \xff\xfe." }, "summary"},
		{"no provider", func(s *Summary) { s.Provider = "" }, "provider"},
		{"no model", func(s *Summary) { s.Model = "" }, "model"},
		{"no prompt version", func(s *Summary) { s.PromptVersion = 0 }, "prompt_version"},
		{"bad content hash", func(s *Summary) { s.ContentHash = nil }, "content_hash"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSummary()
			tt.edit(&s)
			assertInvalidField(t, s.Validate(), tt.field)
		})
	}

	exact := validSummary()
	exact.Text = strings.Repeat("a", MinSummaryLen)
	if err := exact.Validate(); err != nil {
		t.Errorf("summary of exactly %d characters rejected: %v", MinSummaryLen, err)
	}
	exact.Text = strings.Repeat("é", MaxSummaryLen) // characters, not bytes
	if err := exact.Validate(); err != nil {
		t.Errorf("summary of exactly %d characters rejected: %v", MaxSummaryLen, err)
	}
}
