package domain

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Embedding is a vector representation of one item's text, produced by an
// embedding model. It lives beside the item, never inside it.
type Embedding struct {
	ItemID uuid.UUID
	// Provenance: which provider and model produced it, from which item
	// content (the item's ContentHash at the time).
	Provider    string
	Model       string
	Dimensions  int
	Vector      []float32
	ContentHash []byte
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// MaxEmbeddingDimensions bounds the vector length. It matches the schema.
const MaxEmbeddingDimensions = 4096

// Validate checks the vector and its provenance. Provider output must pass
// it before anything is stored.
func (e Embedding) Validate() error {
	var v validator
	v.check(e.ItemID != uuid.Nil, "item_id", "is required")
	v.check(e.Provider != "" && len(e.Provider) <= 32, "provider", "is required")
	v.check(strings.TrimSpace(e.Model) != "" && len(e.Model) <= maxProvenanceText, "model", "is required")
	v.check(e.Dimensions >= 1 && e.Dimensions <= MaxEmbeddingDimensions, "dimensions",
		fmt.Sprintf("must be 1 to %d, got %d", MaxEmbeddingDimensions, e.Dimensions))
	v.check(len(e.Vector) == e.Dimensions, "embedding",
		fmt.Sprintf("has %d values, want %d", len(e.Vector), e.Dimensions))
	finite, nonZero := true, false
	for _, x := range e.Vector {
		if f := float64(x); math.IsNaN(f) || math.IsInf(f, 0) {
			finite = false
		}
		if x != 0 {
			nonZero = true
		}
	}
	v.check(finite, "embedding", "must contain only finite values")
	// A zero vector has no direction, so no similarity can be computed.
	v.check(nonZero || len(e.Vector) == 0, "embedding", "must not be all zeros")
	v.check(len(e.ContentHash) == HashSize, "content_hash", "must be a SHA-256 hash")
	return v.err()
}

// CosineSimilarity returns the cosine of the angle between a and b, from -1
// (opposite) to 1 (same direction). It is computed in float64. Vectors of
// different lengths, empty vectors and zero vectors have no defined
// similarity and return an error.
func CosineSimilarity(a, b []float32) (float64, error) {
	if len(a) != len(b) || len(a) == 0 {
		return 0, fmt.Errorf("%w: cosine similarity of vectors with %d and %d values", ErrInvalid, len(a), len(b))
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0, fmt.Errorf("%w: cosine similarity of a zero vector", ErrInvalid)
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb)), nil
}
