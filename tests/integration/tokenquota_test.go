package integration_test

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/plate-platform/kovern/api/v1alpha1"
)

// setTQStatus fetches the latest version then retries on conflict — the
// controller reconciles concurrently so optimistic locking conflicts are expected.
func setTQStatus(t *testing.T, name, namespace string, state v1alpha1.QuotaState, spentUSD string, nextRenewal *metav1.Time) {
	t.Helper()
	for attempt := 0; attempt < 5; attempt++ {
		tq := &v1alpha1.TokenQuota{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, tq); err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		tq.Status.State = state
		tq.Status.SpentUSD = spentUSD
		if nextRenewal != nil {
			tq.Status.NextRenewal = nextRenewal
		}
		if err := k8sClient.Status().Update(ctx, tq); err == nil {
			return
		} else if !isConflict(err) {
			t.Fatalf("status update %s: %v", name, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("status update %s: too many conflicts", name)
}

func isConflict(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "Conflict") ||
		strings.Contains(err.Error(), "object has been modified"))
}

func makeTokenQuota(name, namespace, saName string, budget string, interval v1alpha1.RenewalInterval) *v1alpha1.TokenQuota {
	return &v1alpha1.TokenQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1alpha1.TokenQuotaSpec{
			TargetRef: v1alpha1.TargetRef{
				Kind: "ServiceAccount",
				Name: saName,
			},
			BillingScope: v1alpha1.BillingScope{
				MaxFinancialBudget: resource.MustParse(budget),
				RenewalInterval:    interval,
				SoftLimitPct:       80,
			},
		},
	}
}

// TestTokenQuota_NextRenewalSetOnCreate verifies that the controller sets
// status.nextRenewal on the first reconcile after creation.
func TestTokenQuota_NextRenewalSetOnCreate(t *testing.T) {
	ns := "default"
	tq := makeTokenQuota("tq-renewal", ns, "agent-renewal", "10", v1alpha1.RenewalDaily)
	if err := k8sClient.Create(ctx, tq); err != nil {
		t.Fatalf("create TokenQuota: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, tq) })

	poll(t, 10*time.Second, 200*time.Millisecond, func() bool {
		got := &v1alpha1.TokenQuota{}
		_ = k8sClient.Get(ctx, types.NamespacedName{Name: "tq-renewal", Namespace: ns}, got)
		return got.Status.NextRenewal != nil
	})

	got := &v1alpha1.TokenQuota{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "tq-renewal", Namespace: ns}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.NextRenewal == nil {
		t.Fatal("expected NextRenewal to be set")
	}
	if got.Status.State != v1alpha1.QuotaStateActive {
		t.Errorf("expected state Active, got %q", got.Status.State)
	}
	// For Daily renewal the next renewal should be ~24h in the future.
	if got.Status.NextRenewal.Time.Before(time.Now().Add(23 * time.Hour)) {
		t.Errorf("NextRenewal too soon: %v", got.Status.NextRenewal.Time)
	}
}

// TestTokenQuota_LedgerSyncedFromStatus verifies that patching the status to
// Exceeded causes the ledger cache to reflect that state after reconcile.
func TestTokenQuota_LedgerSyncedFromStatus(t *testing.T) {
	ns := "default"
	tq := makeTokenQuota("tq-exceeded", ns, "agent-exceeded", "5", v1alpha1.RenewalMonthly)
	if err := k8sClient.Create(ctx, tq); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, tq) })

	// Wait for first reconcile (NextRenewal set).
	poll(t, 10*time.Second, 200*time.Millisecond, func() bool {
		got := &v1alpha1.TokenQuota{}
		_ = k8sClient.Get(ctx, types.NamespacedName{Name: "tq-exceeded", Namespace: ns}, got)
		return got.Status.NextRenewal != nil
	})

	// Patch status to Exceeded (simulates OTel consumer exhausting the budget).
	setTQStatus(t, "tq-exceeded", ns, v1alpha1.QuotaStateExceeded, "5.50", nil)

	// Wait for the controller to reconcile the status change and sync the ledger.
	poll(t, 10*time.Second, 200*time.Millisecond, func() bool {
		state, _ := testCache.Check(ns, "agent-exceeded")
		return state == v1alpha1.QuotaStateExceeded
	})

	state, err := testCache.Check(ns, "agent-exceeded")
	if err != nil {
		t.Fatalf("cache check: %v", err)
	}
	if state != v1alpha1.QuotaStateExceeded {
		t.Errorf("expected Exceeded in cache, got %q", state)
	}
}

// TestTokenQuota_RenewalResetsState verifies that the controller resets state
// to Active and advances NextRenewal when the renewal deadline has passed.
func TestTokenQuota_RenewalResetsState(t *testing.T) {
	ns := "default"
	tq := makeTokenQuota("tq-reset", ns, "agent-reset", "10", v1alpha1.RenewalHourly)
	if err := k8sClient.Create(ctx, tq); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, tq) })

	// Wait for initial reconcile.
	poll(t, 10*time.Second, 200*time.Millisecond, func() bool {
		got := &v1alpha1.TokenQuota{}
		_ = k8sClient.Get(ctx, types.NamespacedName{Name: "tq-reset", Namespace: ns}, got)
		return got.Status.NextRenewal != nil
	})

	// Force NextRenewal into the past and state to Exceeded.
	past := metav1.NewTime(time.Now().Add(-1 * time.Second))
	setTQStatus(t, "tq-reset", ns, v1alpha1.QuotaStateExceeded, "12.00", &past)

	// Controller should detect the past renewal and reset.
	// SpentUSD may be "0" (immediately after reset) or "0.000000" (after the
	// next reconcile reformats it via strconv.FormatFloat) — accept both.
	poll(t, 15*time.Second, 300*time.Millisecond, func() bool {
		got := &v1alpha1.TokenQuota{}
		_ = k8sClient.Get(ctx, types.NamespacedName{Name: "tq-reset", Namespace: ns}, got)
		spent := got.Status.SpentUSD
		return got.Status.State == v1alpha1.QuotaStateActive &&
			(spent == "0" || spent == "0.000000")
	})

	got := &v1alpha1.TokenQuota{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "tq-reset", Namespace: ns}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.State != v1alpha1.QuotaStateActive {
		t.Errorf("expected Active after renewal, got %q", got.Status.State)
	}
	if got.Status.SpentUSD != "0" && got.Status.SpentUSD != "0.000000" {
		t.Errorf("expected SpentUSD=0 after renewal, got %q", got.Status.SpentUSD)
	}
	if got.Status.NextRenewal.Time.Before(time.Now()) {
		t.Errorf("NextRenewal not advanced: %v", got.Status.NextRenewal.Time)
	}
}

// TestTokenQuota_DeleteClearsCache verifies that deleting a TokenQuota removes
// its entry from the ledger cache (fail-open after delete).
func TestTokenQuota_DeleteClearsCache(t *testing.T) {
	ns := "default"
	tq := makeTokenQuota("tq-delete", ns, "agent-delete", "5", v1alpha1.RenewalDaily)
	if err := k8sClient.Create(ctx, tq); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Wait for ledger entry to appear.
	poll(t, 10*time.Second, 200*time.Millisecond, func() bool {
		got := &v1alpha1.TokenQuota{}
		_ = k8sClient.Get(ctx, types.NamespacedName{Name: "tq-delete", Namespace: ns}, got)
		return got.Status.NextRenewal != nil
	})

	if err := k8sClient.Delete(ctx, tq); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// After deletion the controller calls Cache.Delete; Check should fail-open.
	poll(t, 10*time.Second, 200*time.Millisecond, func() bool {
		state, _ := testCache.Check(ns, "agent-delete")
		return state == v1alpha1.QuotaStateActive // Active = fail-open (no entry)
	})

	state, _ := testCache.Check(ns, "agent-delete")
	if state != v1alpha1.QuotaStateActive {
		t.Errorf("expected Active (fail-open) after delete, got %q", state)
	}
}

// TestTokenQuota_SoftLimitSynced verifies the SoftLimit state propagates to cache.
func TestTokenQuota_SoftLimitSynced(t *testing.T) {
	ns := "default"
	tq := makeTokenQuota("tq-softlimit", ns, "agent-softlimit", "10", v1alpha1.RenewalDaily)
	if err := k8sClient.Create(ctx, tq); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, tq) })

	poll(t, 10*time.Second, 200*time.Millisecond, func() bool {
		got := &v1alpha1.TokenQuota{}
		_ = k8sClient.Get(ctx, types.NamespacedName{Name: "tq-softlimit", Namespace: ns}, got)
		return got.Status.NextRenewal != nil
	})

	setTQStatus(t, "tq-softlimit", ns, v1alpha1.QuotaStateSoftLimit, "8.50", nil)

	poll(t, 10*time.Second, 200*time.Millisecond, func() bool {
		state, _ := testCache.Check(ns, "agent-softlimit")
		return state == v1alpha1.QuotaStateSoftLimit
	})

	state, _ := testCache.Check(ns, "agent-softlimit")
	if state != v1alpha1.QuotaStateSoftLimit {
		t.Errorf("expected SoftLimit in cache, got %q", state)
	}
}
