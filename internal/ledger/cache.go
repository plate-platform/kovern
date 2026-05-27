// Package ledger provides an in-memory quota ledger cache for the Kovern admission webhook.
//
// The webhook MUST NOT hit the Kubernetes API on every pod-create request — that would
// add 100-200ms to scheduling latency. Instead, the operator reconciler syncs TokenQuota
// state into this cache, and the webhook reads from it directly.
package ledger

import (
	"fmt"
	"sync"
	"time"

	"github.com/plate-platform/kovern/api/v1alpha1"
)

// Entry is a single quota record held in memory.
type Entry struct {
	// MaxUSD is the hard budget ceiling.
	MaxUSD float64
	// SoftLimitPct is the percentage at which a warning is emitted (0-99).
	SoftLimitPct int
	// SpentUSD is the cumulative spend in the current renewal period.
	SpentUSD float64
	// SpentTokens is the cumulative token count in the current renewal period.
	SpentTokens int64
	// RunCount is the total number of runs recorded.
	RunCount int64
	// State is the current enforcement state.
	State v1alpha1.QuotaState
	// NextRenewal is when the ledger will be reset.
	NextRenewal time.Time
	// EnforcementAction is what to do when the quota is breached.
	EnforcementAction v1alpha1.EnforcementAction
	// UpdatedAt is the last time this entry was modified.
	UpdatedAt time.Time
}

// key returns the cache key for a namespace+serviceAccount pair.
func key(namespace, serviceAccount string) string {
	return namespace + "/" + serviceAccount
}

// Cache is the in-memory quota ledger. It is safe for concurrent use.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]*Entry
	now     func() time.Time // injectable for testing
}

// New creates a new Cache.
func New() *Cache {
	return NewWithClock(time.Now)
}

// NewWithClock creates a Cache with an injectable clock — used in tests.
func NewWithClock(now func() time.Time) *Cache {
	return &Cache{
		entries: make(map[string]*Entry),
		now:     now,
	}
}

// Sync loads or updates a quota entry from a reconciled TokenQuota object.
// Called by the TokenQuota controller on every reconcile.
func (c *Cache) Sync(tq *v1alpha1.TokenQuota) {
	c.mu.Lock()
	defer c.mu.Unlock()

	k := key(tq.Namespace, tq.Spec.TargetRef.Name)

	maxUSD, _ := tq.Spec.BillingScope.MaxFinancialBudget.AsInt64()

	e, exists := c.entries[k]
	if !exists {
		e = &Entry{State: v1alpha1.QuotaStateActive}
		c.entries[k] = e
	}

	e.MaxUSD = float64(maxUSD)
	e.SoftLimitPct = tq.Spec.BillingScope.SoftLimitPct
	if e.SoftLimitPct == 0 {
		e.SoftLimitPct = 80
	}
	e.EnforcementAction = tq.Spec.EnforcementAction

	// Sync current spend from status (source of truth after restarts).
	if tq.Status.State != "" {
		e.State = tq.Status.State
		e.SpentTokens = tq.Status.SpentTokens
		e.RunCount = tq.Status.RunCount
	}

	// Sync renewal timestamp so Check can expire it automatically.
	if tq.Status.NextRenewal != nil {
		e.NextRenewal = tq.Status.NextRenewal.Time
	}

	e.UpdatedAt = c.now()
}

// Check returns the current QuotaState for a given namespace + serviceAccount.
// Returns QuotaStateActive and nil error if no quota is configured (fail-open).
func (c *Cache) Check(namespace, serviceAccount string) (v1alpha1.QuotaState, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	e, ok := c.entries[key(namespace, serviceAccount)]
	if !ok {
		return v1alpha1.QuotaStateActive, nil // no quota configured — allow
	}

	if !e.NextRenewal.IsZero() && c.now().After(e.NextRenewal) {
		// Renewal window has passed — treat as active (controller will reset).
		return v1alpha1.QuotaStateActive, nil
	}

	return e.State, nil
}

// RecordSpend adds a completed run's cost to the ledger and updates the state.
// Returns the new state after the update.
func (c *Cache) RecordSpend(namespace, serviceAccount string, usd float64, tokens int64) (v1alpha1.QuotaState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[key(namespace, serviceAccount)]
	if !ok {
		return v1alpha1.QuotaStateActive, fmt.Errorf("no quota entry for %s/%s", namespace, serviceAccount)
	}

	e.SpentUSD += usd
	e.SpentTokens += tokens
	e.RunCount++
	e.UpdatedAt = c.now()

	e.State = c.computeState(e)
	return e.State, nil
}

// Get returns a copy of the entry for a given namespace + serviceAccount, or nil if not found.
func (c *Cache) Get(namespace, serviceAccount string) *Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	e, ok := c.entries[key(namespace, serviceAccount)]
	if !ok {
		return nil
	}
	copy := *e
	return &copy
}

// Reset clears spend counters for a namespace + serviceAccount (e.g. on renewal).
func (c *Cache) Reset(namespace, serviceAccount string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[key(namespace, serviceAccount)]
	if !ok {
		return
	}
	e.SpentUSD = 0
	e.SpentTokens = 0
	e.RunCount = 0
	e.State = v1alpha1.QuotaStateActive
	e.UpdatedAt = c.now()
}

// Delete removes an entry (e.g. when a TokenQuota is deleted).
func (c *Cache) Delete(namespace, serviceAccount string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key(namespace, serviceAccount))
}

// Len returns the number of entries in the cache.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// computeState derives the QuotaState from an entry's current spend.
// Must be called with the write lock held.
func (c *Cache) computeState(e *Entry) v1alpha1.QuotaState {
	if e.MaxUSD <= 0 {
		return v1alpha1.QuotaStateActive
	}
	pct := (e.SpentUSD / e.MaxUSD) * 100
	switch {
	case pct >= 100:
		return v1alpha1.QuotaStateExceeded
	case float64(e.SoftLimitPct) > 0 && pct >= float64(e.SoftLimitPct):
		return v1alpha1.QuotaStateSoftLimit
	default:
		return v1alpha1.QuotaStateActive
	}
}
