package summary

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// Request is everything sent to Claude for one summary. It's built only by buildRequest, from public
// data (see userMessage).
type Request struct {
	Model     string
	Fallback  string // server-side refusal fallback model, "" = none
	System    string
	User      string
	MaxTokens int64
	Schema    map[string]any
}

// Attempt is one billed attempt (the first model, then possibly its fallback).
type Attempt struct {
	Model        string
	InputTokens  int64
	OutputTokens int64
}

type Response struct {
	Text       string
	StopReason string // end_turn, max_tokens, refusal, …
	Attempts   []Attempt
}

// Claude is the API surface Owl uses; tests use a fake that never spends money.
type Claude interface {
	// CountTokens is free (Anthropic's token-counting endpoint); it's used to price a request exactly
	// before sending it.
	CountTokens(ctx context.Context, r Request) (int64, error)
	Send(ctx context.Context, r Request) (*Response, error)
}

// ErrTransport marks failures where the request may or may not have reached the API (timeouts,
// dropped connections): Owl then books the worst-case cost, so the caps never undercount.
var ErrTransport = errors.New("request failed before a response arrived")

type apiClient struct{ c anthropic.Client }

// NewClient talks to the Claude API with an explicit key (from ANTHROPIC_API_KEY, read by the caller).
func NewClient(apiKey string) Claude {
	return &apiClient{c: anthropic.NewClient(
		option.WithAPIKey(apiKey),
		option.WithMaxRetries(2), // token counting only (free); Send overrides this with 0
		option.WithRequestTimeout(3*time.Minute),
	)}
}

func (a *apiClient) outputConfig(r Request) anthropic.BetaOutputConfigParam {
	return anthropic.BetaOutputConfigParam{
		Effort: "low", // summarizing given text; low keeps thinking (billed as output) small
		Format: anthropic.BetaJSONOutputFormatParam{Schema: r.Schema},
	}
}

func (a *apiClient) CountTokens(ctx context.Context, r Request) (int64, error) {
	res, err := a.c.Beta.Messages.CountTokens(ctx, anthropic.BetaMessageCountTokensParams{
		Model:        anthropic.Model(r.Model),
		System:       anthropic.BetaMessageCountTokensParamsSystemUnion{OfString: anthropic.String(r.System)},
		Messages:     []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(r.User))},
		OutputConfig: a.outputConfig(r),
	})
	if err != nil {
		return 0, err
	}
	return res.InputTokens, nil
}

func (a *apiClient) Send(ctx context.Context, r Request) (*Response, error) {
	p := anthropic.BetaMessageNewParams{
		Model:        anthropic.Model(r.Model),
		MaxTokens:    r.MaxTokens,
		System:       []anthropic.BetaTextBlockParam{{Text: r.System}},
		Messages:     []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(r.User))},
		OutputConfig: a.outputConfig(r),
	}
	if r.Fallback != "" {
		p.Fallbacks = anthropic.BetaFallbacksParamUnion{OfBetaFallbackArray: []anthropic.BetaFallbackParam{{Model: anthropic.Model(r.Fallback)}}}
		p.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
	}
	// No automatic retries for the paid request: a timed-out attempt may still be billed, and a silent
	// retry would be a second request that was never priced against the caps. A failure is booked
	// (at the worst case when the outcome is unknown) and retried by Owl's own rules, at most daily.
	msg, err := a.c.Beta.Messages.New(ctx, p, option.WithMaxRetries(0))
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return nil, err // the API answered with an error status: nothing was generated
		}
		return nil, errors.Join(ErrTransport, err)
	}
	out := &Response{StopReason: string(msg.StopReason)}
	var text strings.Builder
	for _, b := range msg.Content {
		if t, ok := b.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	out.Text = text.String()
	for _, it := range msg.Usage.Iterations {
		if it.Type == "message" || it.Type == "fallback_message" {
			out.Attempts = append(out.Attempts, Attempt{Model: string(it.Model), InputTokens: it.InputTokens + it.CacheReadInputTokens + it.CacheCreationInputTokens, OutputTokens: it.OutputTokens})
		}
	}
	if len(out.Attempts) == 0 {
		out.Attempts = []Attempt{{Model: r.Model, InputTokens: msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens + msg.Usage.CacheCreationInputTokens, OutputTokens: msg.Usage.OutputTokens}}
	}
	return out, nil
}
