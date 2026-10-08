package enrich

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// DefaultAnthropicModel is used when no model is configured.
const DefaultAnthropicModel = "claude-opus-5-5"

// anthropicMaxTokens bounds one response. Thinking counts toward it, so it
// is generous; the summary itself is bounded by validation.
const anthropicMaxTokens = 16000

// AnthropicProvider calls Claude through the Messages API with the official
// SDK. The API key comes from configuration and is sent only in the request
// header; errors carry the status code and request ID, never the key.
type AnthropicProvider struct {
	client anthropic.Client
	model  string
}

// NewAnthropicProvider builds the provider. opts are extra SDK options
// (tests point it at a local server with option.WithBaseURL).
func NewAnthropicProvider(apiKey, model string, opts ...option.RequestOption) *AnthropicProvider {
	if model == "" {
		model = DefaultAnthropicModel
	}
	opts = append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)
	return &AnthropicProvider{client: anthropic.NewClient(opts...), model: model}
}

// Name implements Provider.
func (p *AnthropicProvider) Name() string { return "anthropic" }

// Model implements Provider.
func (p *AnthropicProvider) Model() string { return p.model }

// Complete implements Provider. Short extraction and summary tasks run at
// low effort. Server-side fallbacks ("default") let the API re-serve a
// request a safety classifier declines; a refusal that still stands, or a
// reply cut off at the token limit, is an error rather than output.
func (p *AnthropicProvider) Complete(ctx context.Context, pr Prompt) (string, error) {
	msg, err := p.client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:        anthropic.Model(p.model),
		MaxTokens:    anthropicMaxTokens,
		System:       []anthropic.BetaTextBlockParam{{Text: pr.System}},
		Messages:     []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(pr.User))},
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortLow},
		Fallbacks:    anthropic.BetaFallbacksParamOfDefault(),
		Betas:        []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
	})
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return "", fmt.Errorf("anthropic API: HTTP %d (request %s)", apiErr.StatusCode, apiErr.RequestID)
		}
		return "", fmt.Errorf("anthropic API: %w", err)
	}
	switch msg.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return "", errors.New("anthropic API: the model declined the request")
	case anthropic.BetaStopReasonMaxTokens:
		return "", errors.New("anthropic API: the reply was cut off at the token limit")
	}
	var b strings.Builder
	for _, block := range msg.Content {
		if text, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			b.WriteString(text.Text)
		}
	}
	if b.Len() == 0 {
		return "", errors.New("anthropic API: the reply contained no text")
	}
	return b.String(), nil
}
