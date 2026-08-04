package ledger_test

import (
	"testing"
	"time"

	"github.com/plate-platform/kovern/api/v1alpha1"
	"github.com/plate-platform/kovern/internal/ledger"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func makeQuota(ns, sa, maxUSD string, softPct int) *v1alpha1.TokenQuota {
	return &v1alpha1.TokenQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sa + "-quota",
			Namespace: ns,
		},
		Spec: v1alpha1.TokenQuotaSpec{
			TargetRef: v1alpha1.TargetRef{
				Kind: "ServiceAccount",
				Name: sa,
			},
			BillingScope: v1alpha1.BillingScope{
				MaxFinancialBudget: resource.MustParse(maxUSD),
				SoftLimitPct:       softPct,
			},
			EnforcementAction: v1alpha1.EnforcementSuspendWorkload,
		},
	}
}

func TestNew_EmptyCache(t *testing.T) {
	c := ledger.New()
	if got := c.Len(); got != 0 {
		t.Fatalf("expected 0 entries, got %d", got)
	}
}

func TestCheck_NoEntry_FailOpen(t *testing.T) {
	c := ledger.New()
	state, err := c.Check("team-a", "agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != v1alpha1.QuotaStateActive {
		t.Fatalf("expected Active (fail-open), got %s", state)
	}
}

func TestSync_SetsActiveState(t *testing.T) {
	c := ledger.New()
	tq := makeQuota("team-a", "agent", "100", 80)
	tq.Status.State = v1alpha1.QuotaStateActive
	c.Sync(tq)

	if c.Len() != 1 {
		t.Fatalf("expected 1 entry after sync, got %d", c.Len())
	}
	state, err := c.Check("team-a", "agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != v1alpha1.QuotaStateActive {
		t.Fatalf("expected Active, got %s", state)
	}
}

func TestSync_SetsExceededState(t *testing.T) {
	c := ledger.New()
	tq := makeQuota("team-a", "agent", "100", 80)
	tq.Status.State = v1alpha1.QuotaStateExceeded
	c.Sync(tq)

	state, _ := c.Check("team-a", "agent")
	if state != v1alpha1.QuotaStateExceeded {
		t.Fatalf("expected Exceeded, got %s", state)
	}
}

func TestSync_FractionalBudget_LoadsCorrectMaxUSD(t *testing.T) {
	// Regression test: resource.Quantity.AsInt64() only succeeds for
	// whole-number values — it silently returns (0, false) for "0.50" or
	// any other fractional-dollar budget. Sync used to discard that `ok`
	// via `maxUSD, _ := ...AsInt64()`, so MaxUSD ended up 0 for any
	// non-whole-dollar TokenQuota. computeState treats MaxUSD<=0 as "no
	// budget configured" and never returns Exceeded — meaning a $0.50 (or
	// $9.99, or any other realistic cents-denominated) budget silently had
	// zero enforcement. Reproduced live against a real cluster before
	// fixing: a TokenQuota with maxFinancialBudget "0.50" let spend run
	// past $0.50 without ever flipping to Exceeded.
	c := ledger.New()
	tq := makeQuota("team-a", "agent", "0.50", 80)
	c.Sync(tq)

	e := c.Get("team-a", "agent")
	if e == nil {
		t.Fatal("expected entry, got nil")
	}
	if e.MaxUSD != 0.5 {
		t.Fatalf("expected MaxUSD=0.5, got %v", e.MaxUSD)
	}
}

func TestRecordSpend_FractionalBudget_FlipsToExceeded(t *testing.T) {
	c := ledger.New()
	tq := makeQuota("team-a", "agent", "0.50", 80)
	tq.Status.State = v1alpha1.QuotaStateActive
	c.Sync(tq)

	state, err := c.RecordSpend("team-a", "agent", 0.51, 50000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != v1alpha1.QuotaStateExceeded {
		t.Fatalf("expected Exceeded after $0.51 spend against a $0.50 budget, got %s", state)
	}
}

func TestSync_DefaultSoftLimitPct(t *testing.T) {
	c := ledger.New()
	tq := makeQuota("team-a", "agent", "100", 0) // 0 → default 80
	c.Sync(tq)

	e := c.Get("team-a", "agent")
	if e == nil {
		t.Fatal("expected entry, got nil")
	}
	if e.SoftLimitPct != 80 {
		t.Fatalf("expected SoftLimitPct=80, got %d", e.SoftLimitPct)
	}
}

func TestRecordSpend_SoftLimit(t *testing.T) {
	c := ledger.New()
	tq := makeQuota("team-a", "agent", "100", 80)
	tq.Status.State = v1alpha1.QuotaStateActive
	c.Sync(tq)

	state, err := c.RecordSpend("team-a", "agent", 85.0, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != v1alpha1.QuotaStateSoftLimit {
		t.Fatalf("expected SoftLimit at 85%%, got %s", state)
	}
}

func TestRecordSpend_Exceeded(t *testing.T) {
	c := ledger.New()
	tq := makeQuota("team-a", "agent", "100", 80)
	c.Sync(tq)

	state, err := c.RecordSpend("team-a", "agent", 100.0, 5000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != v1alpha1.QuotaStateExceeded {
		t.Fatalf("expected Exceeded at 100%%, got %s", state)
	}
}

func TestRecordSpend_Accumulates(t *testing.T) {
	c := ledger.New()
	tq := makeQuota("team-a", "agent", "100", 80)
	c.Sync(tq)

	_, _ = c.RecordSpend("team-a", "agent", 45.0, 500)
	state, err := c.RecordSpend("team-a", "agent", 45.0, 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != v1alpha1.QuotaStateSoftLimit {
		t.Fatalf("expected SoftLimit after 90%%, got %s", state)
	}

	e := c.Get("team-a", "agent")
	if e.RunCount != 2 {
		t.Fatalf("expected RunCount=2, got %d", e.RunCount)
	}
	if e.SpentTokens != 1000 {
		t.Fatalf("expected SpentTokens=1000, got %d", e.SpentTokens)
	}
}

func TestRecordSpend_MissingEntry_ReturnsError(t *testing.T) {
	c := ledger.New()
	_, err := c.RecordSpend("team-a", "nobody", 10.0, 100)
	if err == nil {
		t.Fatal("expected error for missing entry, got nil")
	}
}

func TestReset_ClearsSpend(t *testing.T) {
	c := ledger.New()
	tq := makeQuota("team-a", "agent", "100", 80)
	c.Sync(tq)
	_, _ = c.RecordSpend("team-a", "agent", 95.0, 2000)

	c.Reset("team-a", "agent")

	e := c.Get("team-a", "agent")
	if e.SpentUSD != 0 || e.SpentTokens != 0 || e.RunCount != 0 {
		t.Fatalf("expected zeroed spend after reset, got %+v", e)
	}
	if e.State != v1alpha1.QuotaStateActive {
		t.Fatalf("expected Active after reset, got %s", e.State)
	}
}

func TestDelete_RemovesEntry(t *testing.T) {
	c := ledger.New()
	tq := makeQuota("team-a", "agent", "100", 80)
	c.Sync(tq)

	c.Delete("team-a", "agent")

	if c.Len() != 0 {
		t.Fatalf("expected 0 entries after delete, got %d", c.Len())
	}
	// Should fail-open after delete
	state, _ := c.Check("team-a", "agent")
	if state != v1alpha1.QuotaStateActive {
		t.Fatalf("expected Active (fail-open) after delete, got %s", state)
	}
}

func TestGet_ReturnsCopy(t *testing.T) {
	c := ledger.New()
	tq := makeQuota("team-a", "agent", "100", 80)
	c.Sync(tq)

	e := c.Get("team-a", "agent")
	e.SpentUSD = 999 // mutate the copy — must not affect cache

	e2 := c.Get("team-a", "agent")
	if e2.SpentUSD == 999 {
		t.Fatal("Get returned a pointer to internal state instead of a copy")
	}
}

func TestCheck_AfterNextRenewal_FailsOpen(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	past := metav1.NewTime(now.Add(-time.Hour)) // renewal was 1h ago

	c := ledger.NewWithClock(func() time.Time { return now })
	tq := makeQuota("team-a", "agent", "100", 80)
	tq.Status.State = v1alpha1.QuotaStateExceeded
	tq.Status.NextRenewal = &past
	c.Sync(tq)

	state, err := c.Check("team-a", "agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Renewal passed → treat as Active until controller resets
	if state != v1alpha1.QuotaStateActive {
		t.Fatalf("expected Active after renewal window passed, got %s", state)
	}
}

func TestCheck_BeforeNextRenewal_ReturnsExceeded(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	future := metav1.NewTime(now.Add(time.Hour)) // renewal in 1h

	c := ledger.NewWithClock(func() time.Time { return now })
	tq := makeQuota("team-a", "agent", "100", 80)
	tq.Status.State = v1alpha1.QuotaStateExceeded
	tq.Status.NextRenewal = &future
	c.Sync(tq)

	state, _ := c.Check("team-a", "agent")
	if state != v1alpha1.QuotaStateExceeded {
		t.Fatalf("expected Exceeded before renewal, got %s", state)
	}
}

func TestMultipleNamespaces_Isolated(t *testing.T) {
	c := ledger.New()
	c.Sync(makeQuota("ns-a", "agent", "100", 80))
	c.Sync(makeQuota("ns-b", "agent", "50", 70))

	if c.Len() != 2 {
		t.Fatalf("expected 2 entries, got %d", c.Len())
	}

	_, _ = c.RecordSpend("ns-a", "agent", 90.0, 100)
	stateA, _ := c.Check("ns-a", "agent")
	stateB, _ := c.Check("ns-b", "agent")

	if stateA != v1alpha1.QuotaStateSoftLimit {
		t.Fatalf("ns-a: expected SoftLimit, got %s", stateA)
	}
	if stateB != v1alpha1.QuotaStateActive {
		t.Fatalf("ns-b: expected Active (unaffected), got %s", stateB)
	}
}

func TestComputeState_NoMaxUSD_AlwaysActive(t *testing.T) {
	c := ledger.New()
	tq := makeQuota("team-a", "agent", "0", 80)
	c.Sync(tq)

	// Spend should be ignored when MaxUSD=0
	state, _ := c.RecordSpend("team-a", "agent", 1000.0, 99999)
	if state != v1alpha1.QuotaStateActive {
		t.Fatalf("expected Active when MaxUSD=0, got %s", state)
	}
}
