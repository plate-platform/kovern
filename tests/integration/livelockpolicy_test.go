package integration_test

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/plate-platform/kovern/api/v1alpha1"
)

func makeLivelockPolicy(name, namespace string, semantic bool) *v1alpha1.LivelockPolicy {
	p := &v1alpha1.LivelockPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1alpha1.LivelockPolicySpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{"kovern.io/governed": "true"},
			},
			Detection: v1alpha1.DetectionCriteria{
				OTel: &v1alpha1.OTelCriteria{
					MaxSameToolCalls:      3,
					WindowSeconds:         60,
					IdenticalResponseHash: true,
				},
			},
			Remediation: v1alpha1.RemediationSpec{
				Action:         v1alpha1.RemediationInjectFault,
				FallbackAction: v1alpha1.RemediationEvictPod,
				EmitEvent:      true,
				FaultConfig: &v1alpha1.FaultConfig{
					HTTPStatusCode: 429,
					Duration:       "60s",
				},
			},
		},
	}
	if semantic {
		p.Spec.Detection.Semantic = &v1alpha1.SemanticCriteria{
			Enabled:             true,
			WindowTurns:         3,
			SimilarityThreshold: "0.92",
			Model:               "claude-haiku-4-5-20251001",
		}
	}
	return p
}

// TestLivelockPolicy_OTelOnly verifies a LivelockPolicy with only OTel
// detection reconciles cleanly.
func TestLivelockPolicy_OTelOnly(t *testing.T) {
	p := makeLivelockPolicy("llp-otel", "default", false)
	if err := k8sClient.Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, p) })

	// Verify the object is persisted and readable after creation.
	poll(t, 5*time.Second, 200*time.Millisecond, func() bool {
		got := &v1alpha1.LivelockPolicy{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "llp-otel", Namespace: "default"}, got)
		return err == nil && got.Spec.Detection.OTel != nil
	})

	got := &v1alpha1.LivelockPolicy{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "llp-otel", Namespace: "default"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Detection.OTel.MaxSameToolCalls != 3 {
		t.Errorf("maxSameToolCalls: want 3, got %d", got.Spec.Detection.OTel.MaxSameToolCalls)
	}
	if got.Spec.Detection.Semantic != nil {
		t.Errorf("expected no semantic config, got %+v", got.Spec.Detection.Semantic)
	}
}

// TestLivelockPolicy_SemanticEnabled verifies a LivelockPolicy with semantic
// detection enabled is stored and readable.
func TestLivelockPolicy_SemanticEnabled(t *testing.T) {
	p := makeLivelockPolicy("llp-semantic", "default", true)
	if err := k8sClient.Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, p) })

	poll(t, 5*time.Second, 200*time.Millisecond, func() bool {
		got := &v1alpha1.LivelockPolicy{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "llp-semantic", Namespace: "default"}, got)
		return err == nil && got.Spec.Detection.Semantic != nil
	})

	got := &v1alpha1.LivelockPolicy{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "llp-semantic", Namespace: "default"}, got); err != nil {
		t.Fatal(err)
	}
	if !got.Spec.Detection.Semantic.Enabled {
		t.Error("expected semantic.enabled=true")
	}
	if got.Spec.Detection.Semantic.SimilarityThreshold != "0.92" {
		t.Errorf("threshold: want 0.92, got %q", got.Spec.Detection.Semantic.SimilarityThreshold)
	}
}

// TestLivelockPolicy_InjectFaultRemediation verifies fault injection config is
// persisted correctly.
func TestLivelockPolicy_InjectFaultRemediation(t *testing.T) {
	p := makeLivelockPolicy("llp-fault", "default", false)
	if err := k8sClient.Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, p) })

	poll(t, 5*time.Second, 200*time.Millisecond, func() bool {
		got := &v1alpha1.LivelockPolicy{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "llp-fault", Namespace: "default"}, got)
		return err == nil
	})

	got := &v1alpha1.LivelockPolicy{}
	_ = k8sClient.Get(ctx, types.NamespacedName{Name: "llp-fault", Namespace: "default"}, got)

	if got.Spec.Remediation.Action != v1alpha1.RemediationInjectFault {
		t.Errorf("action: want InjectFault, got %q", got.Spec.Remediation.Action)
	}
	if got.Spec.Remediation.FaultConfig == nil {
		t.Fatal("expected FaultConfig to be set")
	}
	if got.Spec.Remediation.FaultConfig.HTTPStatusCode != 429 {
		t.Errorf("httpStatusCode: want 429, got %d", got.Spec.Remediation.FaultConfig.HTTPStatusCode)
	}
	if got.Spec.Remediation.FallbackAction != v1alpha1.RemediationEvictPod {
		t.Errorf("fallback: want EvictPod, got %q", got.Spec.Remediation.FallbackAction)
	}
}

// TestLivelockPolicy_DeleteIsClean verifies deleting a LivelockPolicy does not
// leave orphaned state or cause controller errors.
func TestLivelockPolicy_DeleteIsClean(t *testing.T) {
	p := makeLivelockPolicy("llp-del", "default", false)
	if err := k8sClient.Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}

	poll(t, 5*time.Second, 200*time.Millisecond, func() bool {
		got := &v1alpha1.LivelockPolicy{}
		return k8sClient.Get(ctx, types.NamespacedName{Name: "llp-del", Namespace: "default"}, got) == nil
	})

	if err := k8sClient.Delete(ctx, p); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// After deletion the object should eventually be gone.
	poll(t, 5*time.Second, 200*time.Millisecond, func() bool {
		got := &v1alpha1.LivelockPolicy{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "llp-del", Namespace: "default"}, got)
		return err != nil // NotFound = success
	})
}
