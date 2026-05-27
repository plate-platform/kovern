//go:build integration

package detector_test

import (
	"context"
	"os"
	"testing"

	"github.com/plate-platform/kovern/api/v1alpha1"
	"github.com/plate-platform/kovern/internal/detector"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// loopingTurns simulates an agent stuck calling the same tool with identical results.
var loopingTurns = []string{
	`tool=search_web args={"query":"current weather"} result={"error":"service unavailable"}`,
	`tool=search_web args={"query":"current weather"} result={"error":"service unavailable"}`,
	`tool=search_web args={"query":"current weather"} result={"error":"service unavailable"}`,
}

// diverseTurns simulates healthy agent progress — different tools, different outcomes.
var diverseTurns = []string{
	`tool=search_web args={"query":"London weather"} result={"temp":"18C","conditions":"cloudy"}`,
	`tool=get_calendar args={"date":"today"} result={"events":["standup 9am","review 2pm"]}`,
	`tool=send_email args={"to":"alice@example.com","subject":"weather briefing"} result={"status":"sent"}`,
}

// borderlineTurns uses the same tool but with different args/results — should NOT be livelock.
var borderlineTurns = []string{
	`tool=search_web args={"query":"London weather"} result={"temp":"18C"}`,
	`tool=search_web args={"query":"Paris weather"} result={"temp":"22C"}`,
	`tool=search_web args={"query":"Berlin weather"} result={"temp":"15C"}`,
}

func makeIntegrationSpec(provider, model string) *v1alpha1.LivelockPolicySpec {
	return &v1alpha1.LivelockPolicySpec{
		Selector: metav1.LabelSelector{},
		Detection: v1alpha1.DetectionCriteria{
			Semantic: &v1alpha1.SemanticCriteria{
				Enabled:             true,
				Provider:            provider,
				Model:               model,
				WindowTurns:         3,
				SimilarityThreshold: "0.92",
			},
		},
		Remediation: v1alpha1.RemediationSpec{
			Action: v1alpha1.RemediationInjectFault,
		},
	}
}

// --- Anthropic tests ---

func TestAnthropicDetector_Loop(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	d := detector.New()
	spec := makeIntegrationSpec("Anthropic", "claude-haiku-4-5-20251001")

	ok, reason, err := d.IsLivelocked(context.Background(), loopingTurns, spec)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !ok {
		t.Fatalf("expected livelock detected, got false")
	}
	t.Logf("reason: %s", reason)
}

func TestAnthropicDetector_NoLoop(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	d := detector.New()
	spec := makeIntegrationSpec("Anthropic", "claude-haiku-4-5-20251001")

	ok, _, err := d.IsLivelocked(context.Background(), diverseTurns, spec)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if ok {
		t.Fatal("expected no livelock for diverse turns")
	}
}

func TestAnthropicDetector_BorderlineDifferentArgs(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	d := detector.New()
	spec := makeIntegrationSpec("Anthropic", "claude-haiku-4-5-20251001")

	ok, reason, err := d.IsLivelocked(context.Background(), borderlineTurns, spec)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// Same tool, different cities — should not be livelock. Log result either way.
	t.Logf("borderline result: livelock=%v reason=%q", ok, reason)
}

// --- Gemini tests ---

func TestGeminiDetector_Loop(t *testing.T) {
	if os.Getenv("GOOGLE_API_KEY") == "" {
		t.Skip("GOOGLE_API_KEY not set")
	}
	d, err := detector.NewGemini(os.Getenv("GOOGLE_API_KEY"))
	if err != nil {
		t.Fatalf("NewGemini: %v", err)
	}
	spec := makeIntegrationSpec("Gemini", "gemini-2.5-flash")

	ok, reason, err := d.IsLivelocked(context.Background(), loopingTurns, spec)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !ok {
		t.Fatalf("expected livelock detected, got false")
	}
	t.Logf("reason: %s", reason)
}

func TestGeminiDetector_NoLoop(t *testing.T) {
	if os.Getenv("GOOGLE_API_KEY") == "" {
		t.Skip("GOOGLE_API_KEY not set")
	}
	d, err := detector.NewGemini(os.Getenv("GOOGLE_API_KEY"))
	if err != nil {
		t.Fatalf("NewGemini: %v", err)
	}
	spec := makeIntegrationSpec("Gemini", "gemini-2.5-flash")

	ok, _, err := d.IsLivelocked(context.Background(), diverseTurns, spec)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if ok {
		t.Fatal("expected no livelock for diverse turns")
	}
}

func TestGeminiDetector_BorderlineDifferentArgs(t *testing.T) {
	if os.Getenv("GOOGLE_API_KEY") == "" {
		t.Skip("GOOGLE_API_KEY not set")
	}
	d, err := detector.NewGemini(os.Getenv("GOOGLE_API_KEY"))
	if err != nil {
		t.Fatalf("NewGemini: %v", err)
	}
	spec := makeIntegrationSpec("Gemini", "gemini-2.5-flash")

	ok, reason, err := d.IsLivelocked(context.Background(), borderlineTurns, spec)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	t.Logf("borderline result: livelock=%v reason=%q", ok, reason)
}
