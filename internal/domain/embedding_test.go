package domain

import (
	"crypto/sha256"
	"math"
	"testing"

	"github.com/google/uuid"
)

func validEmbedding() Embedding {
	h := sha256.Sum256([]byte("content"))
	return Embedding{
		ItemID:      uuid.New(),
		Provider:    "fake",
		Model:       "fake-embed-1",
		Dimensions:  3,
		Vector:      []float32{0.6, 0, -0.8},
		ContentHash: h[:],
	}
}

func TestEmbeddingValidate(t *testing.T) {
	if err := validEmbedding().Validate(); err != nil {
		t.Fatalf("valid embedding rejected: %v", err)
	}
	tests := []struct {
		name  string
		edit  func(*Embedding)
		field string
	}{
		{"no item", func(e *Embedding) { e.ItemID = uuid.Nil }, "item_id"},
		{"no provider", func(e *Embedding) { e.Provider = "" }, "provider"},
		{"no model", func(e *Embedding) { e.Model = " " }, "model"},
		{"zero dimensions", func(e *Embedding) { e.Dimensions, e.Vector = 0, nil }, "dimensions"},
		{"too many dimensions", func(e *Embedding) {
			e.Dimensions = MaxEmbeddingDimensions + 1
			e.Vector = make([]float32, e.Dimensions)
			e.Vector[0] = 1
		}, "dimensions"},
		{"too short", func(e *Embedding) { e.Vector = e.Vector[:2] }, "embedding"},
		{"too long", func(e *Embedding) { e.Vector = append(e.Vector, 1) }, "embedding"},
		{"nan", func(e *Embedding) { e.Vector[1] = float32(math.NaN()) }, "embedding"},
		{"infinity", func(e *Embedding) { e.Vector[1] = float32(math.Inf(-1)) }, "embedding"},
		{"all zeros", func(e *Embedding) { e.Vector = []float32{0, 0, 0} }, "embedding"},
		{"bad content hash", func(e *Embedding) { e.ContentHash = []byte{1} }, "content_hash"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEmbedding()
			tt.edit(&e)
			assertInvalidField(t, e.Validate(), tt.field)
		})
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{0.6, 0, -0.8}, []float32{0.6, 0, -0.8}, 1},
		{"scaled", []float32{1, 2}, []float32{2, 4}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite", []float32{1, 1}, []float32{-1, -1}, -1},
	}
	for _, tt := range tests {
		got, err := CosineSimilarity(tt.a, tt.b)
		if err != nil || math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("%s: CosineSimilarity = %v, %v; want %v", tt.name, got, err, tt.want)
		}
	}
	for _, bad := range [][2][]float32{{{1, 0}, {1}}, {{}, {}}, {{0, 0}, {1, 0}}} {
		if _, err := CosineSimilarity(bad[0], bad[1]); err == nil {
			t.Errorf("CosineSimilarity(%v, %v) succeeded, want an error", bad[0], bad[1])
		}
	}
}
