# Kovern

Kubernetes-native governance operator for AI agent workloads.

Kovern adds two controls to the Kubernetes API for agent workloads:

1. **Admission-level budget enforcement** — a `ValidatingAdmissionWebhook` that rejects over-budget pod creates at the Kubernetes API, before scheduling, regardless of the agent's SDK or framework.
2. **Behavioral loop detection** — watches OpenTelemetry spans to detect agents stuck in a repetitive pattern and applies a configurable remediation (fault injection → pod eviction).

Both are optional and fail-open by default. Namespaces that don't have a `TokenQuota` are unaffected. Operators opt in by creating CRs.

---

## How it works

```
Agent pod create
      │
      ▼
┌─────────────────────────┐
│  ValidatingWebhook      │  ← reads in-memory ledger (no K8s API on hot path)
│  vpod.kovern.io         │
│                         │
│  Active  → Allow        │
│  SoftLimit → Allow+Warn │
│  Exceeded → Deny        │
│  Suspended → Deny       │
└─────────────────────────┘

Agent pod running
      │  (emits OTLP spans)
      ▼
OTel Collector → Kovern telemetry consumer
                        │
                        ▼
             TokenQuota.status.spentUSD  (patched after each span batch)
             LivelockPolicy detection    (heuristic: same tool N× in window)
```

The ledger cache is synced from `TokenQuota.status` on every reconcile. The webhook reads exclusively from the cache — no Kubernetes API calls on the admission hot path.

---

## Quick start

### Prerequisites

- Kubernetes cluster (Docker Desktop, kind, or GKE/EKS/AKS)
- Helm 3
- cert-manager (for production TLS) **or** use the self-signed cert path (local dev)

### Install (cert-manager, production)

```bash
helm upgrade --install kovern oci://ghcr.io/plate-platform/charts/kovern \
  --namespace kovern-system --create-namespace \
  --wait
```

### Install (Docker Desktop, local dev)

```bash
git clone https://github.com/plate-platform/kovern
cd kovern

# Build image, generate self-signed certs, install chart
make local-deploy
```

`make local-deploy` handles everything: Docker build, image import into Docker Desktop's containerd store, cert generation, and Helm install.

### Verify

```bash
kubectl get pods -n kovern-system
# NAME                      READY   STATUS    RESTARTS
# kovern-<hash>             1/1     Running   0

kubectl get crd | grep kovern.io
# livelockpolicies.kovern.io
# tokenquotas.kovern.io
```

---

## CRDs

### TokenQuota

Enforces a financial budget on a ServiceAccount. New pod creates are denied once the budget is exceeded.

```yaml
apiVersion: kovern.io/v1alpha1
kind: TokenQuota
metadata:
  name: billing-agent-quota
  namespace: team-payments
spec:
  targetRef:
    kind: ServiceAccount
    name: billing-agent          # pods running as this SA are governed
  billingScope:
    maxFinancialBudget: "50"     # hard cap in USD per renewal period
    renewalInterval: Monthly     # Hourly | Daily | Monthly
    softLimitPct: 80             # emit K8s Warning Event at 80% spend
  enforcementAction: SuspendWorkload  # SuspendWorkload | ScaleToZero | Evict
```

**Status fields:**

| Field | Description |
|---|---|
| `state` | `Active` / `SoftLimit` / `Exceeded` / `Suspended` |
| `spentUSD` | Total cost recorded this renewal period |
| `spentTokens` | Total token count this renewal period |
| `runCount` | Total agent runs recorded |
| `nextRenewal` | When the ledger will automatically reset |

```bash
kubectl get tq -A
# NAME                   STATE    SPENT     BUDGET   RENEWAL   AGE
# billing-agent-quota    Active   12.40     50       Monthly   3d
```

### LivelockPolicy

Detects agents stuck in behavioral loops and applies a configurable remedy.

```yaml
apiVersion: kovern.io/v1alpha1
kind: LivelockPolicy
metadata:
  name: payments-loop-guard
  namespace: team-payments
spec:
  selector:
    matchLabels:
      app.kubernetes.io/component: agent
  detection:
    otel:
      maxSameToolCalls: 3       # same tool called 3+ times in the window
      windowSeconds: 60
      identicalResponseHash: true
    # semantic:                 # enable with ANTHROPIC_API_KEY in kovern-system
    #   enabled: true
    #   windowTurns: 3
    #   similarityThreshold: "0.92"
    #   model: claude-haiku-4-5-20251001
  remediation:
    action: InjectFault         # InjectFault | EvictPod | SuspendWorkload
    faultConfig:
      httpStatusCode: 429
      duration: "60s"
    fallbackAction: EvictPod
    emitEvent: true
```

**Semantic detection** uses Claude to evaluate whether the last N turns of an agent's message history are semantically equivalent loops — not just identical strings. It requires `ANTHROPIC_API_KEY` to be set in the Kovern operator environment and is disabled by default.

```bash
kubectl get llp -A
# NAME                 DETECTIONS   LAST DETECTION   SEMANTIC   AGE
# payments-loop-guard  3            2m ago           false      1d
```

---

## Integration with kagent

Kovern governs any agent pod — it works with kagent, standalone pods, or any other agent framework. No changes to kagent are required.

**Step 1** — create a namespace and a `TokenQuota` targeting the agent's ServiceAccount (kagent names the SA after the `Agent` resource):

```yaml
apiVersion: kovern.io/v1alpha1
kind: TokenQuota
metadata:
  name: my-agent-quota
  namespace: team-payments
spec:
  targetRef:
    kind: ServiceAccount
    name: my-agent          # must match the kagent Agent name
  billingScope:
    maxFinancialBudget: "10"
    renewalInterval: Daily
```

**Step 2** — create the kagent `Agent` with Kovern labels for the `LivelockPolicy` selector:

```yaml
apiVersion: kagent.dev/v1alpha2
kind: Agent
metadata:
  name: my-agent
  namespace: team-payments
  labels:
    kovern.io/team: payments
    kovern.io/governed: "true"
spec:
  type: Declarative
  declarative:
    runtime: python
    modelConfig: default-model-config
    systemMessage: "You are a helpful payments assistant."
    tools: []
```

**Step 3** — verify the webhook enforces:

```bash
# Exhaust the budget manually (for testing)
kubectl patch tq my-agent-quota -n team-payments --type=merge --subresource=status \
  --patch='{"status":{"spentUSD":"10.50","state":"Exceeded"}}'

# Try to create a pod with that SA — should be denied
kubectl run test --image=busybox --restart=Never \
  --overrides='{"spec":{"serviceAccountName":"my-agent"}}' \
  -n team-payments -- sleep 5

# Error from server (Forbidden): admission webhook "vpod.kovern.io" denied the request:
# kovern: quota Exceeded for team-payments/my-agent — no new agent runs until the next renewal period
```

---

## Helm chart values

| Value | Default | Description |
|---|---|---|
| `image.repository` | `ghcr.io/plate-platform/kovern` | Operator image |
| `image.tag` | Chart appVersion | Image tag |
| `image.pullPolicy` | `IfNotPresent` | Pull policy |
| `operator.leaderElect` | `false` | Enable leader election (set `true` for HA) |
| `operator.metricsPort` | `8080` | Prometheus metrics port |
| `operator.healthPort` | `8081` | `/healthz` and `/readyz` port |
| `webhook.port` | `9443` | Admission webhook TLS port |
| `webhook.failurePolicy` | `Ignore` | `Ignore` (fail-open) or `Fail` (fail-closed) |
| `webhook.timeoutSeconds` | `5` | Webhook request timeout |
| `webhook.caBundle` | `""` | Base64 CA cert for webhook TLS verification |
| `tls.selfSigned` | `true` | Generate self-signed cert at startup |
| `tls.certDir` | `/tmp/k8s-webhook-server/serving-certs` | TLS cert directory |
| `tls.secretName` | `kovern-webhook-tls` | Secret name when `selfSigned=false` |
| `anthropic.apiKeySecret.name` | `""` | Secret containing the Anthropic API key |
| `anthropic.apiKeySecret.key` | `api-key` | Key within the Secret |

**Enable semantic loop detection:**

```bash
kubectl create secret generic anthropic-key \
  --from-literal=api-key=sk-ant-... \
  -n kovern-system

helm upgrade kovern ./charts/kovern \
  --set anthropic.apiKeySecret.name=anthropic-key
```

Then set `spec.detection.semantic.enabled: true` in your `LivelockPolicy`.

---

## Local development

```bash
# Run tests (no cluster, no API keys needed)
make test

# Run with coverage report
make test-coverage

# Run operator locally against current kubeconfig context (no webhook)
make run

# Build binary
make build

# Build + load Docker image into Docker Desktop
make local-image-load

# Full local deploy (build → certs → helm install)
make local-deploy

# Tear down
make local-clean
```

### Re-deploying after code changes

```bash
make local-image-load   # rebuild + import into Docker Desktop
kubectl rollout restart deployment/kovern -n kovern-system
```

---

## Architecture

```
kovern/
├── api/v1alpha1/
│   ├── tokenquota_types.go       TokenQuota CRD type + status
│   ├── livelockpolicy_types.go   LivelockPolicy CRD type + status
│   └── zz_generated_deepcopy.go  generated DeepCopy methods
├── cmd/main.go                   operator entry point, flag parsing, manager setup
├── internal/
│   ├── ledger/
│   │   └── cache.go              in-memory quota ledger (RWMutex, synced from CRD status)
│   ├── webhook/
│   │   └── admission.go          ValidatingAdmissionWebhook — reads ledger, denies/warns
│   ├── controller/
│   │   ├── tokenquota_controller.go      reconciles TokenQuota, drives renewal, syncs ledger
│   │   └── livelockpolicy_controller.go  reconciles LivelockPolicy (Phase 2: detection engine)
│   └── claude/
│       └── detector.go           Detector interface + AnthropicDetector + NoOpDetector
├── charts/kovern/                Helm chart (CRDs, Deployment, RBAC, WebhookConfiguration)
├── docs/adr/                     Architecture Decision Records
└── hack/
    └── gen-certs.sh              self-signed cert generation for local dev
```

Key design invariants:
- The admission webhook never calls the Kubernetes API on the hot path — it reads only from the in-memory ledger.
- Missing `TokenQuota` → fail-open (allow). Operators opt in explicitly.
- The ledger is the source of truth between reconciles; `TokenQuota.status` is the durable source.

---

## Architecture Decision Records

| ADR | Decision |
|---|---|
| [001](docs/adr/001-controller-runtime.md) | Use controller-runtime v0.24 as operator framework |
| [002](docs/adr/002-serviceaccount-scope.md) | Scope `TokenQuota` to ServiceAccount, not Namespace |
| [003](docs/adr/003-in-memory-ledger-cache.md) | In-memory ledger cache for zero-latency webhook reads |
| [004](docs/adr/004-claude-semantic-detector.md) | Claude semantic detector behind an interface + NoOp default |
