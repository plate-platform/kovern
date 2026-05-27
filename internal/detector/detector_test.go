package detector_test

import (
	"context"
	"testing"

	"github.com/plate-platform/kovern/api/v1alpha1"
	"github.com/plate-platform/kovern/internal/detector"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// mockDetector is a test double for detector.Detector.
type mockDetector struct {
	livelock bool
	reason   string
	err      error
}

func (m *mockDetector) IsLivelocked(_ context.Context, _ []string, _ *v1alpha1.LivelockPolicySpec) (bool, string, error) {
	return m.livelock, m.reason, m.err
}

// Compile-time interface check.
var _ detector.Detector = (*mockDetector)(nil)

func makePolicySpec(provider string, model string, enabled bool, turns int) *v1alpha1.LivelockPolicySpec {
	return &v1alpha1.LivelockPolicySpec{
		Selector: metav1.LabelSelector{},
		Detection: v1alpha1.DetectionCriteria{
			Semantic: &v1alpha1.SemanticCriteria{
				Enabled:             enabled,
				Provider:            provider,
				Model:               model,
				WindowTurns:         turns,
				SimilarityThreshold: "0.92",
			},
		},
		Remediation: v1alpha1.RemediationSpec{
			Action:    v1alpha1.RemediationInjectFault,
			EmitEvent: true,
			FaultConfig: &v1alpha1.FaultConfig{
				HTTPStatusCode: 429,
				Duration:       "60s",
			},
		},
	}
}

func TestMockDetector_LivelockDetected(t *testing.T) {
	d := &mockDetector{livelock: true, reason: "same tool called repeatedly"}
	spec := makePolicySpec("Anthropic", "claude-haiku-4-5-20251001", true, 3)

	turns := []string{
		`tool=search_web args={"query":"current weather"}`,
		`tool=search_web args={"query":"current weather"}`,
		`tool=search_web args={"query":"current weather"}`,
	}

	ok, reason, err := d.IsLivelocked(context.Background(), turns, spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected livelock=true")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestMockDetector_NoLivelock(t *testing.T) {
	d := &mockDetector{livelock: false}
	spec := makePolicySpec("Anthropic", "claude-haiku-4-5-20251001", true, 3)

	turns := []string{
		`tool=search_web args={"query":"weather London"}`,
		`tool=get_calendar args={"date":"today"}`,
		`tool=send_email args={"to":"alice@example.com"}`,
	}

	ok, _, err := d.IsLivelocked(context.Background(), turns, spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected no livelock for diverse turns")
	}
}

func TestNoOpDetector_DisabledSemantic(t *testing.T) {
	noOp := detector.NewNoOpDetector()
	spec := makePolicySpec("Anthropic", "claude-haiku-4-5-20251001", false, 3)

	ok, _, err := noOp.IsLivelocked(context.Background(), []string{"turn1", "turn2"}, spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected no livelock when semantic detection is disabled")
	}
}

func TestNoOpDetector_TooFewTurns(t *testing.T) {
	noOp := detector.NewNoOpDetector()
	spec := makePolicySpec("Anthropic", "claude-haiku-4-5-20251001", true, 3)

	ok, _, err := noOp.IsLivelocked(context.Background(), []string{"single turn"}, spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected no livelock with fewer than 2 turns")
	}
}

func TestMockDetector_ErrorPropagated(t *testing.T) {
	d := &mockDetector{err: context.DeadlineExceeded}
	spec := makePolicySpec("Gemini", "gemini-2.0-flash", true, 3)

	_, _, err := d.IsLivelocked(context.Background(), []string{"t1", "t2"}, spec)
	if err == nil {
		t.Fatal("expected error to be propagated")
	}
}
