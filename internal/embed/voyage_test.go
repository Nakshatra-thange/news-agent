package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVoyageProvider(t *testing.T) {
	vec := make([]float32, VoyageDimensions)
	vec[0] = 1
	var got voyageRequest
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		switch got.Input[0] {
		case "unauthorized":
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"detail":"Provided API key is invalid."}`)
		case "two":
			fmt.Fprint(w, `{"data":[{"embedding":[1],"index":0},{"embedding":[1],"index":1}]}`)
		case "garbage":
			fmt.Fprint(w, `not json`)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"embedding": vec, "index": 0}}})
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	p := NewVoyageProvider("pa-secret-key", "", srv.URL)
	if p.Name() != "voyage" || p.Model() != DefaultVoyageModel || p.Dimensions() != VoyageDimensions {
		t.Errorf("provider = %s %s %d", p.Name(), p.Model(), p.Dimensions())
	}
	out, err := p.Embed(ctx, "Agents at Scale")
	if err != nil || len(out) != VoyageDimensions || out[0] != 1 {
		t.Fatalf("Embed = %d values, %v", len(out), err)
	}
	if auth != "Bearer pa-secret-key" || got.Model != DefaultVoyageModel || got.InputType != "document" ||
		got.OutputDimension != VoyageDimensions || len(got.Input) != 1 || got.Input[0] != "Agents at Scale" {
		t.Errorf("request = %+v (auth %q)", got, auth)
	}

	for input, want := range map[string]string{
		"unauthorized": "HTTP 401: Provided API key is invalid.",
		"two":          "got 2 embeddings, want 1",
		"garbage":      "malformed response",
	} {
		_, err := p.Embed(ctx, input)
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "pa-secret-key") {
			t.Errorf("Embed(%q) error = %v, want %q without the key", input, err, want)
		}
	}

	srv.Close()
	if _, err := p.Embed(ctx, "x"); err == nil || strings.Contains(err.Error(), "pa-secret-key") {
		t.Errorf("unreachable server error = %v", err)
	}
}
