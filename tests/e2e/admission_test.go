package e2e_test

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/plate-platform/kovern/api/v1alpha1"
)

// setQuotaState fetches the latest resourceVersion and retries on conflict,
// which is necessary because the controller may reconcile concurrently.
func setQuotaState(t *testing.T, name string, state v1alpha1.QuotaState, spentUSD string) {
	t.Helper()
	for attempt := 0; attempt < 5; attempt++ {
		var tq v1alpha1.TokenQuota
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: e2eNamespace}, &tq); err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		tq.Status.State = state
		tq.Status.SpentUSD = spentUSD
		if err := k8sClient.Status().Update(ctx, &tq); err == nil {
			return
		} else if !apierrors.IsConflict(err) {
			t.Fatalf("set quota state %s: %v", name, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("set quota state %s: too many conflicts", name)
}

func makeQuota(name, saName string, budget string) *v1alpha1.TokenQuota {
	return &v1alpha1.TokenQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: e2eNamespace,
		},
		Spec: v1alpha1.TokenQuotaSpec{
			TargetRef: v1alpha1.TargetRef{
				Kind: "ServiceAccount",
				Name: saName,
			},
			BillingScope: v1alpha1.BillingScope{
				MaxFinancialBudget: resource.MustParse(budget),
				RenewalInterval:    v1alpha1.RenewalDaily,
				SoftLimitPct:       80,
			},
		},
	}
}

// waitForLedgerSync waits until the webhook reflects the expected quota state.
// It does this by attempting to create a pod and checking if the result matches
// the expected outcome (denied or allowed). The pod is cleaned up after each attempt.
func waitForExpectedAdmission(t *testing.T, podName, saName string, expectDenied bool, containsMsg string) {
	t.Helper()
	var lastResult string
	poll(t, testTimeout, pollInterval, func() bool {
		pod := makePod(podName, e2eNamespace, saName)
		err := k8sClient.Create(ctx, pod)
		if err == nil {
			// Pod was admitted — clean up and check if that was expected.
			cleanupPod(podName, e2eNamespace)
			lastResult = "admitted"
			return !expectDenied
		}
		// Pod was denied or some other error.
		cleanupPod(podName, e2eNamespace)
		lastResult = err.Error()
		if expectDenied && (apierrors.IsForbidden(err) || strings.Contains(err.Error(), "denied")) {
			if containsMsg == "" || strings.Contains(err.Error(), containsMsg) {
				return true
			}
		}
		return false
	})

	if expectDenied && !strings.Contains(lastResult, "denied") {
		t.Errorf("expected pod to be denied, last result: %s", lastResult)
	}
}

// TestAdmission_NoQuota_FailOpen verifies that a pod is allowed when no
// TokenQuota exists for the ServiceAccount (fail-open contract).
func TestAdmission_NoQuota_FailOpen(t *testing.T) {
	ensureSA(t, "sa-failopen", e2eNamespace)
	t.Cleanup(func() { cleanupPod("pod-failopen", e2eNamespace) })

	pod := makePod("pod-failopen", e2eNamespace, "sa-failopen")
	if err := k8sClient.Create(ctx, pod); err != nil {
		t.Errorf("expected pod to be admitted (fail-open), got: %v", err)
	}
}

// TestAdmission_Active_Allowed verifies that a pod is allowed when the
// TokenQuota state is Active.
func TestAdmission_Active_Allowed(t *testing.T) {
	ensureSA(t, "sa-active", e2eNamespace)
	tq := makeQuota("tq-active", "sa-active", "10")
	if err := k8sClient.Create(ctx, tq); err != nil {
		t.Fatalf("create TokenQuota: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, tq)
		cleanupPod("pod-active", e2eNamespace)
	})

	// Wait for controller to reconcile and set Active state.
	poll(t, testTimeout, pollInterval, func() bool {
		got := &v1alpha1.TokenQuota{}
		_ = k8sClient.Get(ctx, types.NamespacedName{Name: "tq-active", Namespace: e2eNamespace}, got)
		return got.Status.State == v1alpha1.QuotaStateActive
	})

	pod := makePod("pod-active", e2eNamespace, "sa-active")
	if err := k8sClient.Create(ctx, pod); err != nil {
		t.Errorf("expected pod to be admitted with Active quota, got: %v", err)
	}
}

// TestAdmission_Exceeded_Denied verifies that a pod create is denied when the
// TokenQuota state is Exceeded.
func TestAdmission_Exceeded_Denied(t *testing.T) {
	ensureSA(t, "sa-exceeded", e2eNamespace)
	tq := makeQuota("tq-exceeded", "sa-exceeded", "5")
	if err := k8sClient.Create(ctx, tq); err != nil {
		t.Fatalf("create TokenQuota: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, tq) })

	// Wait for initial reconcile then set to Exceeded.
	poll(t, testTimeout, pollInterval, func() bool {
		got := &v1alpha1.TokenQuota{}
		_ = k8sClient.Get(ctx, types.NamespacedName{Name: "tq-exceeded", Namespace: e2eNamespace}, got)
		return got.Status.NextRenewal != nil
	})
	setQuotaState(t, "tq-exceeded", v1alpha1.QuotaStateExceeded, "5.50")

	// Poll until the webhook reflects the Exceeded state and denies the pod.
	waitForExpectedAdmission(t, "pod-exceeded", "sa-exceeded", true, "quota Exceeded")
}

// TestAdmission_Suspended_Denied verifies that a pod create is denied when the
// TokenQuota state is Suspended.
func TestAdmission_Suspended_Denied(t *testing.T) {
	ensureSA(t, "sa-suspended", e2eNamespace)
	tq := makeQuota("tq-suspended", "sa-suspended", "5")
	if err := k8sClient.Create(ctx, tq); err != nil {
		t.Fatalf("create TokenQuota: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, tq) })

	poll(t, testTimeout, pollInterval, func() bool {
		got := &v1alpha1.TokenQuota{}
		_ = k8sClient.Get(ctx, types.NamespacedName{Name: "tq-suspended", Namespace: e2eNamespace}, got)
		return got.Status.NextRenewal != nil
	})
	setQuotaState(t, "tq-suspended", v1alpha1.QuotaStateSuspended, "5.00")

	waitForExpectedAdmission(t, "pod-suspended", "sa-suspended", true, "quota Suspended")
}

// TestAdmission_SoftLimit_Allowed verifies that a pod is admitted when the
// TokenQuota state is SoftLimit (soft limit is a warning, not a denial).
func TestAdmission_SoftLimit_Allowed(t *testing.T) {
	ensureSA(t, "sa-softlimit", e2eNamespace)
	tq := makeQuota("tq-softlimit", "sa-softlimit", "10")
	if err := k8sClient.Create(ctx, tq); err != nil {
		t.Fatalf("create TokenQuota: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, tq)
		cleanupPod("pod-softlimit", e2eNamespace)
	})

	poll(t, testTimeout, pollInterval, func() bool {
		got := &v1alpha1.TokenQuota{}
		_ = k8sClient.Get(ctx, types.NamespacedName{Name: "tq-softlimit", Namespace: e2eNamespace}, got)
		return got.Status.NextRenewal != nil
	})
	setQuotaState(t, "tq-softlimit", v1alpha1.QuotaStateSoftLimit, "8.50")

	// Poll until ledger reflects SoftLimit, then confirm pod is allowed.
	poll(t, testTimeout, pollInterval, func() bool {
		pod := makePod("pod-softlimit", e2eNamespace, "sa-softlimit")
		err := k8sClient.Create(ctx, pod)
		if err == nil {
			return true // admitted — correct
		}
		cleanupPod("pod-softlimit", e2eNamespace)
		if strings.Contains(err.Error(), "denied") {
			t.Errorf("pod was denied at SoftLimit state (should be allowed with warning): %v", err)
			return true // stop polling on unexpected denial
		}
		return false
	})
}

// TestAdmission_DefaultSA_FailOpen verifies that a pod with no explicit
// ServiceAccount (defaults to "default") is allowed when no quota exists for it.
func TestAdmission_DefaultSA_FailOpen(t *testing.T) {
	t.Cleanup(func() { cleanupPod("pod-defaultsa", e2eNamespace) })

	// No TokenQuota for the "default" SA in this namespace.
	pod := makePod("pod-defaultsa", e2eNamespace, "")
	if err := k8sClient.Create(ctx, pod); err != nil {
		t.Errorf("expected pod with default SA to be admitted (fail-open), got: %v", err)
	}
}

// TestAdmission_QuotaResetAllowsPod verifies that after a quota is reset from
// Exceeded to Active, new pods are admitted again.
func TestAdmission_QuotaResetAllowsPod(t *testing.T) {
	ensureSA(t, "sa-reset", e2eNamespace)
	tq := makeQuota("tq-reset", "sa-reset", "5")
	if err := k8sClient.Create(ctx, tq); err != nil {
		t.Fatalf("create TokenQuota: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, tq)
		cleanupPod("pod-reset", e2eNamespace)
	})

	poll(t, testTimeout, pollInterval, func() bool {
		got := &v1alpha1.TokenQuota{}
		_ = k8sClient.Get(ctx, types.NamespacedName{Name: "tq-reset", Namespace: e2eNamespace}, got)
		return got.Status.NextRenewal != nil
	})

	// Step 1: exceed the budget.
	setQuotaState(t, "tq-reset", v1alpha1.QuotaStateExceeded, "5.50")
	waitForExpectedAdmission(t, "pod-reset", "sa-reset", true, "quota Exceeded")

	// Step 2: reset the budget (simulates renewal or manual reset).
	setQuotaState(t, "tq-reset", v1alpha1.QuotaStateActive, "0")

	// Step 3: pod should now be admitted.
	poll(t, testTimeout, pollInterval, func() bool {
		pod := makePod("pod-reset", e2eNamespace, "sa-reset")
		err := k8sClient.Create(ctx, pod)
		if err == nil {
			return true
		}
		cleanupPod("pod-reset", e2eNamespace)
		return false
	})

	t.Log("pod admitted after quota reset")

	// Wait a bit before cleanup to avoid race with kubelet.
	time.Sleep(200 * time.Millisecond)
}
