package ingest

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"synergy/internal/canon"
	"synergy/internal/domain"
	"synergy/internal/sources/sourcestest"
)

var (
	testSourceID = uuid.MustParse("0199a000-0000-7000-8000-0000000000aa")
	testNow      = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
)

func ptr[T any](v T) *T { return &v }

func validCandidate() domain.Candidate {
	return domain.Candidate{
		ExternalID: "42", Kind: domain.ItemKindDiscussion, Title: "A title",
		URL: "https://example.com/post",
	}
}

func TestNormalizeHappyPath(t *testing.T) {
	c := domain.Candidate{
		ExternalID:    " 2410.12345 ",
		Kind:          domain.ItemKindPaper,
		Title:         "  Scaling\n Laws  ",
		Description:   "Line one.\nLine two.",
		URL:           " https://arxiv.org/pdf/2410.12345v2.pdf ",
		DiscussionURL: "https://news.ycombinator.com/item?id=1",
		Authors:       []string{" Ada ", "Ada", "", "Alan"},
		Tags:          []string{"cs.LG", "cs.LG", " cs.AI"},
		Metadata:      json.RawMessage(`{"stars": 12345678901234567890, "nested": {"list": ["a\u0000b"]}}`),
		PublishedAt:   ptr(time.Date(2026, 10, 5, 10, 0, 0, 0, time.FixedZone("CEST", 2*3600))),
	}
	n, err := Normalize(testSourceID, c, testNow)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if n.SourceID != testSourceID || n.ExternalID != "2410.12345" || n.Title != "Scaling Laws" ||
		n.Description != "Line one. Line two." || n.URL != "https://arxiv.org/pdf/2410.12345v2.pdf" {
		t.Errorf("text fields = %+v", n)
	}
	if n.CanonicalURL != "https://arxiv.org/abs/2410.12345" || !bytes.Equal(n.URLHash, canon.URLHash(n.CanonicalURL)) {
		t.Errorf("canonical = %q", n.CanonicalURL)
	}
	if !bytes.Equal(n.ContentHash, canon.ContentHash("Scaling Laws", "Line one. Line two.")) {
		t.Error("content hash not computed over normalized text")
	}
	if !slices.Equal(n.Authors, []string{"Ada", "Alan"}) || !slices.Equal(n.Tags, []string{"cs.LG", "cs.AI"}) {
		t.Errorf("authors=%q tags=%q", n.Authors, n.Tags)
	}
	if n.DiscussionURL != "https://news.ycombinator.com/item?id=1" {
		t.Errorf("discussion = %q", n.DiscussionURL)
	}
	if n.PublishedAt == nil || n.PublishedAt.Location() != time.UTC || n.PublishedAt.Hour() != 8 {
		t.Errorf("PublishedAt = %v, want 08:00 UTC", n.PublishedAt)
	}
	// Big integers keep their exact form; NUL is stripped from nested strings.
	if !strings.Contains(string(n.Metadata), "12345678901234567890") || strings.Contains(string(n.Metadata), `\u0000`) {
		t.Errorf("metadata = %s", n.Metadata)
	}
}

func TestNormalizeRejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.Candidate)
		want   string
	}{
		{"missing external id", func(c *domain.Candidate) { c.ExternalID = "  " }, "external_id"},
		{"huge external id", func(c *domain.Candidate) { c.ExternalID = strings.Repeat("x", 600) }, "external_id"},
		{"unknown kind", func(c *domain.Candidate) { c.Kind = "tweet" }, "kind"},
		{"empty kind", func(c *domain.Candidate) { c.Kind = "" }, "kind"},
		{"blank title", func(c *domain.Candidate) { c.Title = " \n\t " }, "title"},
		{"control-only title", func(c *domain.Candidate) { c.Title = "\x00\x01" }, "title"},
		{"missing url", func(c *domain.Candidate) { c.URL = "" }, "invalid url"},
		{"relative url", func(c *domain.Candidate) { c.URL = "/item?id=1" }, "invalid url"},
		{"javascript url", func(c *domain.Candidate) { c.URL = "javascript:alert(1)" }, "invalid url"},
		{"huge url", func(c *domain.Candidate) { c.URL = "https://example.com/" + strings.Repeat("a", 5000) }, "url longer"},
		{"metadata array", func(c *domain.Candidate) { c.Metadata = json.RawMessage(`[1,2]`) }, "metadata"},
		{"metadata null", func(c *domain.Candidate) { c.Metadata = json.RawMessage(`null`) }, "metadata"},
		{"metadata malformed", func(c *domain.Candidate) { c.Metadata = json.RawMessage(`{"a":`) }, "metadata"},
		{"metadata too large", func(c *domain.Candidate) {
			c.Metadata = json.RawMessage(`{"x":"` + strings.Repeat("a", maxMetadataBytes) + `"}`)
		}, "metadata larger"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validCandidate()
			tt.mutate(&c)
			_, err := Normalize(testSourceID, c, testNow)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestNormalizeRepairs(t *testing.T) {
	c := validCandidate()
	c.Title = strings.Repeat("é", maxTitleLen) // 2 bytes each: over the limit
	c.Description = strings.Repeat("d", maxDescriptionLen+10)
	c.DiscussionURL = "not a url"
	c.Authors = make([]string, maxAuthors+20)
	for i := range c.Authors {
		c.Authors[i] = strings.Repeat("a", i+1)
	}
	c.Metadata = json.RawMessage(`  `)

	n, err := Normalize(testSourceID, c, testNow)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(n.Title) > maxTitleLen || !strings.HasPrefix(c.Title, n.Title) {
		t.Errorf("title not truncated on a rune boundary: %d bytes", len(n.Title))
	}
	if len(n.Description) != maxDescriptionLen {
		t.Errorf("description = %d bytes, want %d", len(n.Description), maxDescriptionLen)
	}
	if n.DiscussionURL != "" {
		t.Errorf("invalid discussion URL kept: %q", n.DiscussionURL)
	}
	if len(n.Authors) != maxAuthors {
		t.Errorf("authors = %d, want capped at %d", len(n.Authors), maxAuthors)
	}
	if string(n.Metadata) != "{}" {
		t.Errorf("blank metadata = %s, want {}", n.Metadata)
	}
}

func TestNormalizePublishedAtPlausibility(t *testing.T) {
	tests := []struct {
		name string
		in   *time.Time
		keep bool
	}{
		{"nil", nil, false},
		{"zero", ptr(time.Time{}), false},
		{"recent", ptr(testNow.Add(-time.Hour)), true},
		{"slightly ahead (clock skew)", ptr(testNow.Add(2 * time.Hour)), true},
		{"far future", ptr(testNow.Add(48 * time.Hour)), false},
		{"before 1990", ptr(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)), false},
		{"old arXiv paper", ptr(time.Date(1995, 6, 1, 0, 0, 0, 0, time.UTC)), true},
	}
	for _, tt := range tests {
		c := validCandidate()
		c.PublishedAt = tt.in
		n, err := Normalize(testSourceID, c, testNow)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if (n.PublishedAt != nil) != tt.keep {
			t.Errorf("%s: PublishedAt = %v, keep=%v", tt.name, n.PublishedAt, tt.keep)
		}
	}
}

func TestContentHashIgnoresWhitespaceButNotWording(t *testing.T) {
	a, b := validCandidate(), validCandidate()
	a.Title, a.Description = "Hello  world", "Body\ntext"
	b.Title, b.Description = " Hello world ", "Body text"
	na, _ := Normalize(testSourceID, a, testNow)
	nb, _ := Normalize(testSourceID, b, testNow)
	if !bytes.Equal(na.ContentHash, nb.ContentHash) {
		t.Error("whitespace-only differences changed the content hash")
	}
	b.Title = "Hello world!"
	nb, _ = Normalize(testSourceID, b, testNow)
	if bytes.Equal(na.ContentHash, nb.ContentHash) {
		t.Error("a wording change did not change the content hash")
	}
	// Metadata is not content: it must not affect the hash.
	b.Title, b.Metadata = a.Title, json.RawMessage(`{"points": 999}`)
	nb, _ = Normalize(testSourceID, b, testNow)
	if !bytes.Equal(na.ContentHash, nb.ContentHash) {
		t.Error("metadata affected the content hash")
	}
}

func TestNormalizeBatchFixture(t *testing.T) {
	cands := sourcestest.LoadCandidates(t, "testdata/hn_batch.json")
	items, rejected, folded := normalizeBatch(testSourceID, cands, testNow)

	if len(cands) != 7 || folded != 1 {
		t.Fatalf("candidates=%d folded=%d, want 7 and 1", len(cands), folded)
	}
	gotRejected := map[string]string{}
	for _, r := range rejected {
		gotRejected[r.ExternalID] = r.Reason
	}
	if len(rejected) != 2 || !strings.Contains(gotRejected["41000004"], "title") || !strings.Contains(gotRejected["41000005"], "invalid url") {
		t.Errorf("rejected = %+v, want the blank title and the javascript: URL", rejected)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}

	byID := map[string]domain.NewItem{}
	for _, it := range items {
		byID[it.ExternalID] = it
	}
	if got := byID["41000001"].CanonicalURL; got != "https://arxiv.org/abs/2410.12345" {
		t.Errorf("arXiv link canonical = %q", got)
	}
	if got := byID["41000002"].CanonicalURL; got != "https://github.com/acme/agent-kit" {
		t.Errorf("GitHub link canonical = %q", got)
	}
	blog := byID["41000003"]
	if blog.CanonicalURL != "https://blog.example.com/rlhf" || blog.Title != "An independent blog post about RLHF" ||
		blog.Description != "text with a NUL byte" || !slices.Equal(blog.Authors, []string{"alice"}) || !slices.Equal(blog.Tags, []string{"rlhf"}) {
		t.Errorf("blog item = %+v", blog)
	}
	if strings.Contains(string(blog.Metadata), `\u0000`) {
		t.Errorf("NUL survived in metadata: %s", blog.Metadata)
	}
	if byID["41000006"].PublishedAt != nil {
		t.Error("far-future date kept")
	}
	// The folded repeat must not have replaced the first occurrence.
	if strings.Contains(blog.Title, "folded") {
		t.Error("in-batch repeat overwrote the first occurrence")
	}
}
