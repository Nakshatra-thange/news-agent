package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Enrichment is structured intelligence an LLM extracted from one item. It
// lives beside the item, never inside it.
type Enrichment struct {
	ItemID     uuid.UUID
	Topics     []string
	Entities   []Entity
	Importance int
	Category   Category
	// Provenance: which provider, model and prompt version produced it, from
	// which item content (the item's ContentHash at the time).
	Provider      string
	Model         string
	PromptVersion int
	ContentHash   []byte
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Entity is a named thing an item is about.
type Entity struct {
	Name string     `json:"name"`
	Type EntityType `json:"type"`
}

// EntityType classifies an entity.
type EntityType string

// EntityTypes lists the accepted entity types.
var EntityTypes = []EntityType{"person", "organization", "product", "model", "technology", "other"}

// Category classifies what kind of development an item is.
type Category string

// Categories lists the accepted categories.
var Categories = []Category{
	"research", "model_release", "tool", "product_news", "industry", "tutorial", "opinion", "other",
}

// Enrichment bounds.
const (
	MinImportance     = 1
	MaxImportance     = 5
	MaxTopics         = 8
	MaxTopicLen       = 40
	MaxEntities       = 20
	MaxEntityNameLen  = 100
	maxProvenanceText = 100
)

// Normalize trims and lowercases topics, trims entity names and drops empty
// or repeated values, so equivalent outputs store identically.
func (e *Enrichment) Normalize() {
	topics := make([]string, 0, len(e.Topics))
	for _, t := range e.Topics {
		t = strings.ToLower(strings.Join(strings.Fields(t), " "))
		if t != "" && !slices.Contains(topics, t) {
			topics = append(topics, t)
		}
	}
	e.Topics = topics

	entities := make([]Entity, 0, len(e.Entities))
	for _, en := range e.Entities {
		en.Name = strings.Join(strings.Fields(en.Name), " ")
		en.Type = EntityType(strings.ToLower(strings.TrimSpace(string(en.Type))))
		if en.Name != "" && !slices.Contains(entities, en) {
			entities = append(entities, en)
		}
	}
	e.Entities = entities
	e.Category = Category(strings.ToLower(strings.TrimSpace(string(e.Category))))
}

// Validate checks every field against the enrichment bounds. Model output
// must pass it before anything is stored.
func (e Enrichment) Validate() error {
	var v validator
	v.check(e.ItemID != uuid.Nil, "item_id", "is required")

	v.check(len(e.Topics) >= 1 && len(e.Topics) <= MaxTopics, "topics", fmt.Sprintf("must have 1 to %d entries", MaxTopics))
	for _, t := range e.Topics {
		if t == "" || utf8.RuneCountInString(t) > MaxTopicLen {
			v.check(false, "topics", fmt.Sprintf("each topic must be 1 to %d characters", MaxTopicLen))
			break
		}
	}

	v.check(len(e.Entities) <= MaxEntities, "entities", fmt.Sprintf("must have at most %d entries", MaxEntities))
	for _, en := range e.Entities {
		if en.Name == "" || utf8.RuneCountInString(en.Name) > MaxEntityNameLen {
			v.check(false, "entities", fmt.Sprintf("each name must be 1 to %d characters", MaxEntityNameLen))
			break
		}
		if !slices.Contains(EntityTypes, en.Type) {
			v.check(false, "entities", fmt.Sprintf("unknown entity type %q", en.Type))
			break
		}
	}

	v.check(e.Importance >= MinImportance && e.Importance <= MaxImportance, "importance",
		fmt.Sprintf("must be an integer from %d to %d", MinImportance, MaxImportance))
	v.check(slices.Contains(Categories, e.Category), "category", fmt.Sprintf("unknown category %q", e.Category))

	v.check(e.Provider != "" && len(e.Provider) <= 32, "provider", "is required")
	v.check(strings.TrimSpace(e.Model) != "" && len(e.Model) <= maxProvenanceText, "model", "is required")
	v.check(e.PromptVersion > 0, "prompt_version", "must be positive")
	v.check(len(e.ContentHash) == HashSize, "content_hash", "must be a SHA-256 hash")
	return v.err()
}
