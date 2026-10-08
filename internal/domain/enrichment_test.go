package domain

import (
	"crypto/sha256"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func validEnrichment() Enrichment {
	h := sha256.Sum256([]byte("content"))
	return Enrichment{
		ItemID:        uuid.New(),
		Topics:        []string{"agents"},
		Entities:      []Entity{{Name: "Anthropic", Type: "organization"}},
		Importance:    3,
		Category:      "research",
		Provider:      "fake",
		Model:         "fake-1",
		PromptVersion: 1,
		ContentHash:   h[:],
	}
}

func TestEnrichmentNormalize(t *testing.T) {
	e := validEnrichment()
	e.Topics = []string{"  Code   Generation ", "code generation", "", "Agents"}
	e.Entities = []Entity{{Name: " OpenAI ", Type: " Organization"}, {Name: "OpenAI", Type: "organization"}, {Name: "  ", Type: "other"}}
	e.Category = " Research "
	e.Normalize()
	if !slices.Equal(e.Topics, []string{"code generation", "agents"}) {
		t.Errorf("topics = %q", e.Topics)
	}
	if !slices.Equal(e.Entities, []Entity{{Name: "OpenAI", Type: "organization"}}) {
		t.Errorf("entities = %+v", e.Entities)
	}
	if e.Category != "research" {
		t.Errorf("category = %q", e.Category)
	}
	if err := e.Validate(); err != nil {
		t.Errorf("normalized enrichment invalid: %v", err)
	}
}

func TestEnrichmentValidate(t *testing.T) {
	if err := validEnrichment().Validate(); err != nil {
		t.Fatalf("valid enrichment rejected: %v", err)
	}
	many := func(n int, s string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = s + strings.Repeat("x", i)
		}
		return out
	}
	tests := []struct {
		name  string
		edit  func(*Enrichment)
		field string
	}{
		{"no item", func(e *Enrichment) { e.ItemID = uuid.Nil }, "item_id"},
		{"no topics", func(e *Enrichment) { e.Topics = nil }, "topics"},
		{"too many topics", func(e *Enrichment) { e.Topics = many(MaxTopics+1, "t") }, "topics"},
		{"topic too long", func(e *Enrichment) { e.Topics = []string{strings.Repeat("a", MaxTopicLen+1)} }, "topics"},
		{"too many entities", func(e *Enrichment) {
			e.Entities = nil
			for _, n := range many(MaxEntities+1, "e") {
				e.Entities = append(e.Entities, Entity{Name: n, Type: "other"})
			}
		}, "entities"},
		{"entity without name", func(e *Enrichment) { e.Entities = []Entity{{Type: "person"}} }, "entities"},
		{"entity name too long", func(e *Enrichment) {
			e.Entities = []Entity{{Name: strings.Repeat("n", MaxEntityNameLen+1), Type: "person"}}
		}, "entities"},
		{"unknown entity type", func(e *Enrichment) { e.Entities = []Entity{{Name: "X", Type: "company"}} }, "entities"},
		{"importance too low", func(e *Enrichment) { e.Importance = 0 }, "importance"},
		{"importance too high", func(e *Enrichment) { e.Importance = 6 }, "importance"},
		{"unknown category", func(e *Enrichment) { e.Category = "gossip" }, "category"},
		{"no provider", func(e *Enrichment) { e.Provider = "" }, "provider"},
		{"no model", func(e *Enrichment) { e.Model = " " }, "model"},
		{"no prompt version", func(e *Enrichment) { e.PromptVersion = 0 }, "prompt_version"},
		{"bad content hash", func(e *Enrichment) { e.ContentHash = []byte{1} }, "content_hash"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEnrichment()
			tt.edit(&e)
			assertInvalidField(t, e.Validate(), tt.field)
		})
	}
}
