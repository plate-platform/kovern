# ADR 002 — Scope TokenQuota to ServiceAccount, not namespace labels

**Status:** Accepted  
**Date:** 2026-05-26

## Context

Quota enforcement requires a way to identify which workloads are governed. Two common approaches:

1. **Namespace labels** — attach quota to a namespace; all pods in the namespace share the budget.
2. **ServiceAccount** — attach quota to a specific Kubernetes ServiceAccount; only pods using that SA are governed.

## Decision

Scope `TokenQuota.spec.targetRef` to a **ServiceAccount** within a namespace.

The admission webhook checks `pod.spec.serviceAccountName` and looks up the quota for the `(namespace, serviceAccountName)` pair.

## Consequences

**Positive:**
- **Fine-grained**: different agent teams in the same namespace can have separate budgets (`billing-agent` vs `research-agent`).
- **K8s-native**: ServiceAccounts are already used for RBAC and workload identity; attaching quota to them follows the principle of least surprise.
- **Multi-tenant namespaces**: a platform team can share one namespace across multiple agent roles with independent budget controls.
- **Framework-agnostic**: every pod has a ServiceAccount. Works regardless of whether the agent uses kagent, plain K8s Jobs, or any other mechanism.

**Negative:**
- Operators must remember to create a dedicated ServiceAccount per agent role (not use `default`).
- The ledger cache key is `namespace/serviceaccount` — slightly more complex than a single namespace key.

## Alternatives not chosen

- **Namespace labels**: simpler but too coarse — a namespace with 10 agent types would need 10 namespaces instead of 1 namespace with 10 ServiceAccounts.
- **Pod labels**: labels can be added/removed by any controller; not tamper-resistant.
