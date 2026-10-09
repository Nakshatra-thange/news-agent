package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Voyage AI defaults. The model must support 1024-dimension output
// (voyage-3.5, voyage-3.5-lite and voyage-3-large do).
const (
	DefaultVoyageModel = "voyage-3.5"
	VoyageDimensions   = 1024
	defaultVoyageURL   = "https://api.voyageai.com/v1/embeddings"
	voyageMaxBody      = 1 << 20
)

// VoyageProvider calls the Voyage AI embeddings API. The API key is sent
// only in the Authorization header; errors carry the status code and the
// API's short error detail, never the key.
type VoyageProvider struct {
	apiKey string
	model  string
	url    string
	client *http.Client
}

// NewVoyageProvider builds the provider. url is the endpoint; empty means
// the public API (tests pass a local server).
func NewVoyageProvider(apiKey, model, url string) *VoyageProvider {
	if model == "" {
		model = DefaultVoyageModel
	}
	if url == "" {
		url = defaultVoyageURL
	}
	return &VoyageProvider{apiKey: apiKey, model: model, url: url, client: &http.Client{Timeout: 30 * time.Second}}
}

// Name implements Provider.
func (p *VoyageProvider) Name() string { return "voyage" }

// Model implements Provider.
func (p *VoyageProvider) Model() string { return p.model }

// Dimensions implements Provider.
func (p *VoyageProvider) Dimensions() int { return VoyageDimensions }

type voyageRequest struct {
	Input           []string `json:"input"`
	Model           string   `json:"model"`
	InputType       string   `json:"input_type"`
	OutputDimension int      `json:"output_dimension"`
}

type voyageResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Detail string `json:"detail"`
}

// Embed implements Provider. The text is embedded as a "document".
func (p *VoyageProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(voyageRequest{
		Input: []string{text}, Model: p.model, InputType: "document", OutputDimension: VoyageDimensions,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("voyage API: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voyage API: %w", err) // the URL holds no credential
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, voyageMaxBody))
	if err != nil {
		return nil, fmt.Errorf("voyage API: read response: %w", err)
	}
	var out voyageResponse
	decErr := json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(out.Detail)
		if r := []rune(detail); len(r) > 200 {
			detail = string(r[:200])
		}
		if detail != "" {
			return nil, fmt.Errorf("voyage API: HTTP %d: %s", resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("voyage API: HTTP %d", resp.StatusCode)
	}
	if decErr != nil {
		return nil, fmt.Errorf("voyage API: malformed response: %w", decErr)
	}
	if len(out.Data) != 1 || out.Data[0].Index != 0 {
		return nil, fmt.Errorf("voyage API: got %d embeddings, want 1", len(out.Data))
	}
	return out.Data[0].Embedding, nil
}
