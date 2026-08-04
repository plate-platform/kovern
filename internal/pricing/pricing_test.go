package pricing_test

import (
	"path"
	"testing"

	"github.com/plate-platform/kovern/internal/pricing"
)

func TestEstimate_ExactGlobMatch(t *testing.T) {
	e := pricing.NewEstimator(map[string]pricing.ModelPrice{
		"claude-haiku-4-5*": {InputPer1K: 0.00025, OutputPer1K: 0.00125},
	})

	usd, matched := e.Estimate("claude-haiku-4-5-20251001", 1000, 1000)
	if !matched {
		t.Fatal("expected a matched price entry")
	}
	want := 0.00025 + 0.00125
	if diff := usd - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("expected usd=%v, got %v", want, usd)
	}
}

func TestEstimate_MostSpecificPatternWins(t *testing.T) {
	e := pricing.NewEstimator(map[string]pricing.ModelPrice{
		"gpt-4o*":      {InputPer1K: 1.0, OutputPer1K: 1.0}, // deliberately distinct from the specific match
		"gpt-4o-mini*": {InputPer1K: 0.00015, OutputPer1K: 0.0006},
	})

	// Run several times — map iteration order is randomized per-process, so a
	// bug that let map order leak into matching would be flaky, not always wrong.
	for i := 0; i < 20; i++ {
		usd, matched := e.Estimate("gpt-4o-mini-2026", 1000, 1000)
		if !matched {
			t.Fatal("expected a matched price entry")
		}
		want := 0.00015 + 0.0006
		if diff := usd - want; diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("expected the more specific gpt-4o-mini* entry (usd=%v), got %v — pattern specificity ordering is not deterministic", want, usd)
		}
	}
}

func TestEstimate_NoMatch_UsesFallbackNotZero(t *testing.T) {
	e := pricing.NewEstimator(map[string]pricing.ModelPrice{
		"claude-haiku-4-5*": {InputPer1K: 0.00025, OutputPer1K: 0.00125},
	})

	usd, matched := e.Estimate("some-custom-finetune-v3", 1000, 1000)
	if matched {
		t.Fatal("expected matched=false for an unrecognised model")
	}
	if usd <= 0 {
		t.Fatalf("expected a non-zero fallback cost so unrecognised models can't bypass budget enforcement, got %v", usd)
	}
}

func TestEstimate_ZeroTokens_ZeroCost(t *testing.T) {
	e := pricing.NewDefaultEstimator()
	usd, _ := e.Estimate("claude-haiku-4-5-20251001", 0, 0)
	if usd != 0 {
		t.Fatalf("expected 0 cost for 0 tokens, got %v", usd)
	}
}

func TestSet_OverridesExistingPattern(t *testing.T) {
	e := pricing.NewEstimator(map[string]pricing.ModelPrice{
		"claude-haiku-4-5*": {InputPer1K: 0.00025, OutputPer1K: 0.00125},
	})
	e.Set("claude-haiku-4-5*", pricing.ModelPrice{InputPer1K: 1, OutputPer1K: 1})

	usd, matched := e.Estimate("claude-haiku-4-5-20251001", 1000, 0)
	if !matched {
		t.Fatal("expected a matched price entry")
	}
	if usd != 1 {
		t.Fatalf("expected override to take effect (usd=1), got %v", usd)
	}
}

func TestSet_AddsNewPattern(t *testing.T) {
	e := pricing.NewEstimator(map[string]pricing.ModelPrice{})
	e.Set("internal-finetune-*", pricing.ModelPrice{InputPer1K: 0.5, OutputPer1K: 0.5})

	usd, matched := e.Estimate("internal-finetune-v1", 1000, 1000)
	if !matched {
		t.Fatal("expected the newly added pattern to match")
	}
	if usd != 1 {
		t.Fatalf("expected usd=1, got %v", usd)
	}
}

func TestDefaultPrices_PatternsAreValidGlobs(t *testing.T) {
	// path.Match returns ErrBadPattern for malformed patterns (e.g. an
	// unbalanced "[") — catch typos in the built-in table at test time.
	for pattern := range pricing.DefaultPrices() {
		if _, err := path.Match(pattern, "probe"); err != nil {
			t.Errorf("pattern %q is not a valid glob: %v", pattern, err)
		}
	}
}
