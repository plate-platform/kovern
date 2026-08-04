// Package pricing estimates the USD cost of an LLM call from token usage,
// so the OTel receiver can turn gen_ai.usage.* span attributes into a
// concrete spend amount for internal/ledger.Cache.RecordSpend.
//
// Prices are approximate and meant for budget *governance* (deciding when a
// ServiceAccount has burned through its TokenQuota), not for billing
// reconciliation against a provider invoice. Operators with different
// contracted rates should override entries via Estimator.Set.
package pricing

import (
	"path"
	"sort"
)

// ModelPrice is the USD cost per 1,000 tokens for a model or model group.
type ModelPrice struct {
	InputPer1K  float64
	OutputPer1K float64
}

func (p ModelPrice) cost(inputTokens, outputTokens int64) float64 {
	return (float64(inputTokens)/1000)*p.InputPer1K + (float64(outputTokens)/1000)*p.OutputPer1K
}

// fallbackPrice is used when no configured pattern matches the reported
// model name. Without a fallback, an unrecognised model would silently cost
// $0 and its spend would never count against the ServiceAccount's budget —
// exactly the bypass Kovern's admission-level enforcement is meant to close.
// The rate is a conservative mid-tier estimate; Estimate reports matched=false
// so callers can log/alert that a price entry is missing.
var fallbackPrice = ModelPrice{InputPer1K: 0.003, OutputPer1K: 0.015}

// DefaultPrices returns Kovern's built-in price table, keyed by glob pattern
// (matched with path.Match semantics — the same convention documented on
// TokenQuotaSpec.Limits[].ModelGroup, e.g. "claude-*", "gpt-4*"). Approximate
// as of 2026; adjust via Estimator.Set for production billing accuracy.
func DefaultPrices() map[string]ModelPrice {
	return map[string]ModelPrice{
		"claude-haiku-4-5*": {InputPer1K: 0.00025, OutputPer1K: 0.00125},
		"claude-haiku-4.5*": {InputPer1K: 0.00025, OutputPer1K: 0.00125},
		"claude-sonnet-5*":  {InputPer1K: 0.003, OutputPer1K: 0.015},
		"claude-opus-5*":    {InputPer1K: 0.015, OutputPer1K: 0.075},
		"gemini-*-flash*":   {InputPer1K: 0.000075, OutputPer1K: 0.0003},
		"gemini-*-pro*":     {InputPer1K: 0.00125, OutputPer1K: 0.005},
		"gpt-4o-mini*":      {InputPer1K: 0.00015, OutputPer1K: 0.0006},
		"gpt-4o*":           {InputPer1K: 0.0025, OutputPer1K: 0.01},
	}
}

type priceEntry struct {
	pattern string
	price   ModelPrice
}

// Estimator resolves a model name to a USD cost for a given token count.
// It is not safe for concurrent writes (Set) racing with reads (Estimate);
// build the table at startup, before the OTLP receiver starts serving.
type Estimator struct {
	entries  []priceEntry
	fallback ModelPrice
}

// NewEstimator builds an Estimator from a pattern->price table.
func NewEstimator(prices map[string]ModelPrice) *Estimator {
	e := &Estimator{fallback: fallbackPrice}
	for pattern, price := range prices {
		e.entries = append(e.entries, priceEntry{pattern: pattern, price: price})
	}
	e.resort()
	return e
}

// NewDefaultEstimator builds an Estimator from DefaultPrices.
func NewDefaultEstimator() *Estimator {
	return NewEstimator(DefaultPrices())
}

// Set adds or overrides the price for a model group pattern.
func (e *Estimator) Set(pattern string, price ModelPrice) {
	for i, entry := range e.entries {
		if entry.pattern == pattern {
			e.entries[i].price = price
			return
		}
	}
	e.entries = append(e.entries, priceEntry{pattern: pattern, price: price})
	e.resort()
}

// resort orders entries by pattern length, descending, so the most specific
// pattern (e.g. "gpt-4o-mini*") is tried before a broader one that would
// also match (e.g. "gpt-4o*"). This keeps matching deterministic regardless
// of map iteration order.
func (e *Estimator) resort() {
	sort.SliceStable(e.entries, func(i, j int) bool {
		return len(e.entries[i].pattern) > len(e.entries[j].pattern)
	})
}

// Estimate returns the USD cost for a model call. matched reports whether an
// explicit price entry was found; when false, usd is still computed (via a
// conservative fallback rate) rather than returned as zero, so an
// unrecognised model can never silently bypass budget enforcement.
func (e *Estimator) Estimate(model string, inputTokens, outputTokens int64) (usd float64, matched bool) {
	for _, entry := range e.entries {
		if ok, err := path.Match(entry.pattern, model); err == nil && ok {
			return entry.price.cost(inputTokens, outputTokens), true
		}
	}
	return e.fallback.cost(inputTokens, outputTokens), false
}
