# ADR 003 — In-memory quota ledger for the admission webhook hot path

**Status:** Accepted  
**Date:** 2026-05-26

## Context

The `ValidatingAdmissionWebhook` fires on every Pod CREATE in the cluster. If the
webhook makes a synchronous Kubernetes API call on each request, it adds 50–200ms of
latency to pod scheduling — unacceptable for time-sensitive workloads.

Options evaluated:
1. **K8s API call per request** — authoritative but adds latency to every pod create.
2. **Shared informer cache** (controller-runtime `client.Reader`) — cached but still involves internal lock contention and object copies.
3. **Dedicated in-memory ledger** — a `sync.RWMutex`-protected map updated by the controller reconciler; the webhook does a single map lookup.

## Decision

Use a **dedicated in-memory ledger** (`internal/ledger.Cache`).

The TokenQuota reconciler calls `cache.Sync(tq)` on every reconcile. The webhook calls `cache.Check(ns, sa)` — a single RLock + map lookup.

## Consequences

**Positive:**
- Webhook hot path is O(1), sub-microsecond, no network round-trip.
- `NewWithClock` makes the cache fully testable with a fake clock.
- Cache is always eventually consistent — controller reconciles on every TokenQuota event.

**Negative:**
- State is lost on operator restart. Mitigated: the controller immediately reconciles all existing TokenQuotas on startup, repopulating the cache within seconds.
- A very brief window (milliseconds) after restart where the cache is empty and all requests are allowed. This is intentional (fail-open).
- Cache and `TokenQuota.status` can briefly diverge during reconcile. The status is the durable source of truth; the cache is a performance optimisation.

## Cache key

`namespace/serviceAccountName` — same key used in K8s informers for namespaced resources.

## Fail-open contract

`cache.Check` returns `QuotaStateActive` and `nil` error when:
- No entry exists for the namespace+SA (quota not configured for this workload)
- `NextRenewal` has passed (controller is late — allow rather than block indefinitely)

`cache.RecordSpend` returns an error when no entry exists (called by the OTel consumer, not the webhook).
