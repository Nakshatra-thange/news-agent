package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// OpenRouter defaults. The default model is free of charge on OpenRouter
// (with daily and per-minute request limits).
const (
	DefaultOpenRouterModel = "nvidia/nemotron-3.5-lightning:free"
	defaultOpenRouterURL   = "https://openrouter.ai/api/v1/chat/completions"
	openRouterMaxBody      = 1 << 20
	openRouterMaxTokens    = 1024
	// openRouterMaxRetryWait is the longest Retry-After honored when
	// retrying a rate-limited request; longer waits fail with
	// ErrRateLimited so the run stops instead of stalling.
	openRouterMaxRetryWait = 20 * time.Second
)

// openRouterBackoff is the wait before each retry of a rate-limited request
// that names no Retry-After. Free models share an upstream pool that is
// often briefly saturated; failed requests do not use the daily quota.
var openRouterBackoff = []time.Duration{3 * time.Second, 8 * time.Second}

// OpenRouterProvider calls a model through OpenRouter's OpenAI-compatible
// chat completions API (non-streaming). The API key is sent only in the
// Authorization header; errors carry the status code and the API's short
// error message, never the key.
type OpenRouterProvider struct {
	apiKey string
	model  string
	url    string
	client *http.Client
}

// NewOpenRouterProvider builds the provider. url is the endpoint; empty
// means the public API (tests pass a local server).
func NewOpenRouterProvider(apiKey, model, url string) *OpenRouterProvider {
	if model == "" {
		model = DefaultOpenRouterModel
	}
	if url == "" {
		url = defaultOpenRouterURL
	}
	return &OpenRouterProvider{apiKey: apiKey, model: model, url: url, client: &http.Client{Timeout: 50 * time.Second}}
}

// Name implements Provider.
func (p *OpenRouterProvider) Name() string { return "openrouter" }

// Model implements Provider.
func (p *OpenRouterProvider) Model() string { return p.model }

type openRouterMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openRouterRequest struct {
	Model       string              `json:"model"`
	Messages    []openRouterMessage `json:"messages"`
	MaxTokens   int                 `json:"max_tokens"`
	Temperature float64             `json:"temperature"`
	// Reasoning is switched off: a short grounded summary does not need
	// it, and hidden reasoning tokens would count against MaxTokens.
	Reasoning struct {
		Enabled bool `json:"enabled"`
	} `json:"reasoning"`
}

type openRouterError struct {
	Code     any    `json:"code"`
	Message  string `json:"message"`
	Metadata struct {
		// Raw is the upstream provider's own explanation, e.g. that a free
		// model is rate limited in a shared pool.
		Raw any `json:"raw"`
	} `json:"metadata"`
}

type openRouterResponse struct {
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
		} `json:"message"`
		FinishReason string           `json:"finish_reason"`
		Error        *openRouterError `json:"error"`
	} `json:"choices"`
	Error *openRouterError `json:"error"`
}

// Complete implements Provider. A low temperature keeps summaries close to
// the source. A rate-limited request is retried after the server's short
// Retry-After, or else after openRouterBackoff; rate limits that persist
// and unavailable upstreams are ErrRateLimited. A reply cut off at the token limit, an error inside the
// reply or an empty reply is an error rather than output.
func (p *OpenRouterProvider) Complete(ctx context.Context, pr Prompt) (string, error) {
	req := openRouterRequest{
		Model: p.model,
		Messages: []openRouterMessage{
			{Role: "system", Content: pr.System},
			{Role: "user", Content: pr.User},
		},
		MaxTokens:   openRouterMaxTokens,
		Temperature: 0.2,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	status, retryAfter, out, err := p.post(ctx, body)
	for attempt := 0; err == nil && status == http.StatusTooManyRequests && attempt < len(openRouterBackoff); attempt++ {
		wait := openRouterBackoff[attempt]
		if retryAfter > 0 {
			if retryAfter > openRouterMaxRetryWait {
				break
			}
			wait = retryAfter
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
		status, retryAfter, out, err = p.post(ctx, body)
	}
	if err != nil {
		return "", err
	}

	if status != http.StatusOK {
		msg := fmt.Sprintf("openrouter API: HTTP %d%s", status, errorDetail(out.Error))
		if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
			return "", fmt.Errorf("%s: %w", msg, ErrRateLimited)
		}
		return "", errors.New(msg)
	}
	if out.Error != nil {
		return "", fmt.Errorf("openrouter API: error in reply%s", errorDetail(out.Error))
	}
	if len(out.Choices) == 0 {
		return "", errors.New("openrouter API: the reply contained no choices")
	}
	c := out.Choices[0]
	switch {
	case c.Error != nil:
		return "", fmt.Errorf("openrouter API: error in reply%s", errorDetail(c.Error))
	case c.FinishReason == "length":
		return "", errors.New("openrouter API: the reply was cut off at the token limit")
	case c.FinishReason == "content_filter":
		return "", errors.New("openrouter API: the reply was blocked by a content filter")
	case c.Message.Content == nil || strings.TrimSpace(*c.Message.Content) == "":
		return "", errors.New("openrouter API: the reply contained no text")
	}
	return *c.Message.Content, nil
}

// post sends one request and decodes the reply. A non-200 status is not an
// error here; the caller decides. It returns the server's Retry-After.
func (p *OpenRouterProvider) post(ctx context.Context, body []byte) (int, time.Duration, openRouterResponse, error) {
	var out openRouterResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, out, fmt.Errorf("openrouter API: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Title", "Synergy")
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, 0, out, fmt.Errorf("openrouter API: %w", err) // the URL holds no credential
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, openRouterMaxBody))
	if err != nil {
		return 0, 0, out, fmt.Errorf("openrouter API: read response: %w", err)
	}
	decErr := json.Unmarshal(raw, &out)
	if resp.StatusCode == http.StatusOK && decErr != nil {
		return 0, 0, out, fmt.Errorf("openrouter API: malformed response: %w", decErr)
	}
	var wait time.Duration
	if s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && s > 0 {
		wait = time.Duration(s) * time.Second
	}
	return resp.StatusCode, wait, out, nil
}

// errorDetail renders the API's error message, bounded, or nothing.
func errorDetail(e *openRouterError) string {
	if e == nil {
		return ""
	}
	msg := e.Message
	if raw, ok := e.Metadata.Raw.(string); ok && strings.TrimSpace(raw) != "" {
		msg = raw
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	if msg == "" {
		return ""
	}
	return ": " + msg
}
