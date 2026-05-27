# Kovern — CLAUDE.md

Kovern is a Kubernetes-native operator that enforces **financial budgets and behavioural
loop detection** for AI agent workloads. It has two differentiators that don't exist
in any other tool:

1. **K8s admission-level budget enforcement** — a `ValidatingAdmissionWebhook` that
   rejects over-budget pod creates at the control plane, before scheduling, regardless
   of the agent's SDK or framework.
2. **Semantic loop detection** — uses Claude (`claude-haiku-4-5-20251001` by default)
   to detect when an agent is stuck in a semantically equivalent loop, not just a
   hard timeout.

## Repository layout

```
kovern/
├── api/v1alpha1/           CRD types (TokenQuota, LivelockPolicy)
├── cmd/main.go             Operator entry point
├── internal/
│   ├── claude/             Anthropic semantic detector (interface + real impl)
│   ├── controller/         TokenQuota + LivelockPolicy reconcilers
│   ├── ledger/             In-memory quota ledger (webhook reads this, not K8s API)
│   └── webhook/            ValidatingAdmissionWebhook handler
├── charts/kovern/          Helm chart (CRDs, Deployment, RBAC, WebhookConfiguration)
├── docs/adr/               Architecture Decision Records
└── .claude/agents/         Agent role definitions
```

## Key design invariants

- The admission webhook **must not** hit the Kubernetes API on the hot path.
  It reads exclusively from `internal/ledger.Cache`.
- The ledger cache is the **source of truth only between controller reconciles**.
  The `TokenQuota.status` is the durable source of truth and is re-synced to the
  cache on every reconcile and after restarts.
- Fail-open: if no `TokenQuota` exists for a namespace+serviceaccount pair, the
  webhook allows the pod. Operators explicitly opt in by creating a `TokenQuota`.
- Fail-open on cache errors: a ledger read error must never block a legitimate workload.
- The Claude semantic detector is **optional and disabled by default**.
  Setting `spec.detection.semantic.enabled: true` in a `LivelockPolicy` activates it.
  It requires `ANTHROPIC_API_KEY` in the operator environment.

## Running tests

```bash
make test           # all unit tests (no cluster required, no API keys needed)
make test-coverage  # with HTML coverage report
```

The test suite is self-contained. The Claude detector uses the `claude.Detector`
interface with a `NoOpDetector` for unit tests. The webhook tests use a fake
in-memory ledger.

## Local cluster development

```bash
# Install on a local kind or Docker Desktop cluster
make helm-install

# Watch operator logs
kubectl logs -n kovern-system -l app.kubernetes.io/name=kovern -f

# Apply example TokenQuota
kubectl apply -f charts/kovern/examples/tokenquota.yaml

# Check quota state
kubectl get tq -A
```

## Adding a new CRD

1. Add type file to `api/v1alpha1/<name>_types.go`
2. Add `DeepCopyInto`/`DeepCopy`/`DeepCopyObject` to `zz_generated_deepcopy.go`
3. Register in `init()` via `SchemeBuilder.Register`
4. Add reconciler in `internal/controller/`
5. Add Helm template in `charts/kovern/templates/crd-<name>.yaml`
6. Update RBAC in `charts/kovern/templates/rbac.yaml`

## Environment variables

| Variable | Required | Purpose |
|---|---|---|
| `ANTHROPIC_API_KEY` | Only if semantic detection enabled | Claude API key for livelock evaluation |
| `KUBERNETES_SERVICE_HOST` | Auto-injected in cluster | In-cluster K8s client |

## Commit conventions

`<type>(<scope>): <summary>` — types: `feat`, `fix`, `test`, `docs`, `refactor`, `chore`

Examples:
- `feat(webhook): deny pod creates when quota exceeded`
- `fix(ledger): fix race condition in concurrent RecordSpend`
- `test(webhook): add soft-limit warning assertion`
