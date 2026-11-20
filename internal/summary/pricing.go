package summary

import (
	"math"
	"sort"
)

// Price is USD per million tokens. ($ per million tokens = micro-dollars per token.)
type Price struct{ Input, Output float64 }

// prices are Anthropic's first-party API list prices (2026-11). A model not listed here refuses to
// run: Owl never sends a request it can't price.
var prices = map[string]Price{
	"claude-opus-5-5":   {4, 20},
	"claude-opus-5":     {5, 25},
	"claude-opus-4-8":   {5, 25},
	"claude-sonnet-5-5": {2, 10},
	"claude-sonnet-5":   {2, 10},
	"claude-haiku-5-5":  {0.10, 0.50}, // prompts over 100k tokens: see priceFor
	"claude-fable-5-2":  {8, 40},
}

// fallbacks: the server-side refusal fallback used per model (release notes describing CVEs can trip
// the cyber classifier). The fallback attempt bills at its own model's price.
// Claude Haiku 5.5 has no server-side fallback: a refused request fails (and is retried the next day).
var fallbacks = map[string]string{
	"claude-opus-5-5":   "claude-opus-5",
	"claude-sonnet-5-5": "claude-sonnet-5", // the only fallback Sonnet 5.5's array form accepts
}

// DefaultModel is used when neither Settings → Claude summaries nor OWL_CLAUDE_MODEL picks one.
// Summarizing given text is not hard reasoning: Sonnet 5.5 does it well at half Opus 5.5's price.
const DefaultModel = "claude-sonnet-5-5"

// SelectableModels are offered in Settings → Claude summaries, cheapest first. OWL_CLAUDE_MODEL may
// name any priced model as the server default.
var SelectableModels = []string{"claude-haiku-5-5", "claude-sonnet-5-5", "claude-opus-5-5"}

func priceFor(model string, inputTokens int64) (Price, bool) {
	p, ok := prices[model]
	if ok && model == "claude-haiku-5-5" && inputTokens > 100_000 {
		p = Price{0.50, 2.50}
	}
	return p, ok
}

// costMicros is the cost in micro-USD, rounded up.
func costMicros(p Price, in, out int64) int64 {
	return int64(math.Ceil(float64(in)*p.Input + float64(out)*p.Output))
}

// worstCase is the most a request can cost: all input plus max output on the model, plus the same again
// on its fallback (a refusal mid-output is billed and then retried on the fallback).
func worstCase(model string, in, maxOut int64) (int64, bool) {
	p, ok := priceFor(model, in)
	if !ok {
		return 0, false
	}
	total := costMicros(p, in, maxOut)
	if fb := fallbacks[model]; fb != "" {
		fp, ok := priceFor(fb, in)
		if !ok {
			return 0, false
		}
		total += costMicros(fp, in, maxOut)
	}
	return total, true
}

// actualCost prices what the API reported per attempt. An attempt on a model without a known price is
// charged at the most expensive known price (never undercount).
func actualCost(attempts []Attempt) int64 {
	var total int64
	for _, a := range attempts {
		p, ok := priceFor(a.Model, a.InputTokens)
		if !ok {
			p = maxPrice()
		}
		total += costMicros(p, a.InputTokens, a.OutputTokens)
	}
	return total
}

func maxPrice() Price {
	var m Price
	for _, p := range prices {
		m.Input, m.Output = math.Max(m.Input, p.Input), math.Max(m.Output, p.Output)
	}
	return m
}

// estimateTokens is the local, request-free estimate used in "estimate only" mode and for listing:
// deliberately high (about 3 characters per token; real text is closer to 4).
func estimateTokens(chars int) int64 { return int64(chars/3) + promptOverhead }

// promptOverhead covers the system prompt, schema and framing (about 6k characters ≈ 1.5–2k tokens; set
// high on purpose — the exact count from the API is what the caps use).
const promptOverhead = 2000

// KnownModel reports whether Owl can price model (and so enforce the caps for it).
func KnownModel(model string) bool { _, ok := prices[model]; return ok }

func KnownModels() []string {
	var out []string
	for m := range prices {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
