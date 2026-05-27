# ADR 001 — Use controller-runtime for the operator framework

**Status:** Accepted  
**Date:** 2026-05-26

## Context

Kovern needs to watch Kubernetes CRD objects, reconcile them into in-memory state, and serve an admission webhook. We evaluated three options:

1. **controller-runtime** — the de-facto Go operator framework, maintained by the Kubernetes SIG-Controller-Runtime team and used by kubebuilder, Operator SDK, and most production operators.
2. **client-go directly** — lower-level K8s Go client; requires manually wiring watchers, informers, work queues, and leader election.
3. **kopf (Python)** — event-driven operator framework in Python; simpler but mismatches our Go codebase and has fewer production deployments at scale.

## Decision

Use **controller-runtime v0.24** (pinned in go.mod).

## Consequences

**Positive:**
- Built-in webhook server, leader election, health checks, and metrics in one library.
- Reconciler pattern is well-understood by the Kubernetes community; every operator developer knows it.
- Deep integration with kubebuilder markers for RBAC and CRD generation.
- The `admission.Decoder`, `admission.Handler`, `admission.Response.WithWarnings` abstractions make the webhook straightforward.

**Negative:**
- controller-runtime moves quickly; minor API breaks between minor versions.
- The library is a non-trivial dependency tree (~30 transitive deps).

## Alternatives not chosen

- **client-go directly**: too much boilerplate (manual informer cache, work queue, retry logic). controller-runtime builds on client-go anyway, so we get all the primitives.
- **kopf**: Python only; doesn't fit the Go operator binary we need to ship as a single Docker image.
