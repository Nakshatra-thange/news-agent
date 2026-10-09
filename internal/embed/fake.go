package embed

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// FakeDimensions is the FakeProvider's vector length.
const FakeDimensions = 256

// FakeProvider is a deterministic, offline Provider for tests and for
// exercising the pipeline without credentials. It hashes each lowercase
// word of the text into one of FakeDimensions buckets and normalizes the
// counts to unit length, so texts sharing words get similar vectors. It
// captures word overlap only, not meaning; its vectors are stored with
// provider "fake" under their own model.
type FakeProvider struct{}

// Name implements Provider.
func (FakeProvider) Name() string { return "fake" }

// Model implements Provider.
func (FakeProvider) Model() string { return "fake-embed-1" }

// Dimensions implements Provider.
func (FakeProvider) Dimensions() int { return FakeDimensions }

// Embed implements Provider.
func (FakeProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	vec := make([]float64, FakeDimensions)
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, w := range words {
		h := fnv.New32a()
		h.Write([]byte(w))
		vec[h.Sum32()%FakeDimensions]++
	}
	var norm float64
	for _, x := range vec {
		norm += x * x
	}
	if norm == 0 {
		return nil, errors.New("fake: text has no words")
	}
	norm = math.Sqrt(norm)
	out := make([]float32, FakeDimensions)
	for i, x := range vec {
		out[i] = float32(x / norm)
	}
	return out, nil
}
