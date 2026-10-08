package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Summary is a short LLM-written paragraph about one item. It lives beside
// the item, never inside it.
type Summary struct {
	ItemID uuid.UUID
	Text   string
	// Provenance: which provider, model and prompt version wrote it, from
	// which item content (the item's ContentHash at the time).
	Provider      string
	Model         string
	PromptVersion int
	ContentHash   []byte
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Summary length bounds, in characters.
const (
	MinSummaryLen = 40
	MaxSummaryLen = 600
)

// Normalize collapses whitespace (including newlines) to single spaces.
func (s *Summary) Normalize() {
	s.Text = strings.Join(strings.Fields(s.Text), " ")
}

// Validate checks the summary text and provenance. Model output must pass
// it before anything is stored.
func (s Summary) Validate() error {
	var v validator
	v.check(s.ItemID != uuid.Nil, "item_id", "is required")

	n := utf8.RuneCountInString(s.Text)
	v.check(n >= MinSummaryLen && n <= MaxSummaryLen, "summary",
		fmt.Sprintf("must be %d to %d characters, got %d", MinSummaryLen, MaxSummaryLen, n))
	v.check(!strings.ContainsRune(s.Text, '\n'), "summary", "must be a single paragraph")
	v.check(!looksFormatted(s.Text), "summary", "must be plain prose, not Markdown, JSON or code")
	v.check(utf8.ValidString(s.Text), "summary", "must be valid UTF-8")

	v.check(s.Provider != "" && len(s.Provider) <= 32, "provider", "is required")
	v.check(strings.TrimSpace(s.Model) != "" && len(s.Model) <= maxProvenanceText, "model", "is required")
	v.check(s.PromptVersion > 0, "prompt_version", "must be positive")
	v.check(len(s.ContentHash) == HashSize, "content_hash", "must be a SHA-256 hash")
	return v.err()
}

// looksFormatted reports output that is structured rather than a prose
// paragraph: code fences, JSON, Markdown headings or bullets.
func looksFormatted(t string) bool {
	return strings.Contains(t, "```") ||
		strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") ||
		strings.HasPrefix(t, "#") || strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ")
}
