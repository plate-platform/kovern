# Kovern Roadmap

Kovern is a Kubernetes-native governance system for AI agent workloads. This roadmap outlines the completed phases, current state, and upcoming enhancements.

---

## ✅ Phase 1: Budget Enforcement (COMPLETE)

**Status:** Production-ready

Enforces financial budgets for AI agent workloads at the Kubernetes admission level.

### What's Implemented:
- **TokenQuota CRD** — defines per-ServiceAccount budget caps, soft limits, and renewal intervals (Hourly/Daily/Monthly)
- **In-memory Ledger Cache** — fast budget checks on the admission webhook hot path
- **ValidatingAdmissionWebhook** — intercepts Pod CREATE requests, blocks over-budget workloads before scheduling
- **Budget States** — Active, SoftLimit (80%), Exceeded, Suspended
- **Status Sync** — TokenQuota status reflects real-time spend and renewal timings

### How It Works:
```
Agent tries to start → Kubernetes calls webhook → Check TokenQuota ledger
├─ Active    → Allow ✓
├─ SoftLimit → Allow + Warning ⚠️
└─ Exceeded  → Deny ✗ (pod never starts)
```

### Key Invariants:
- Webhook **never** calls the Kubernetes API on the hot path (reads only from ledger cache)
- Fail-open: if no quota exists, pod is allowed
- Fail-open on errors: ledger read failures never block workloads
- Controller regularly reconciles status back to cache on startup and changes

---

## ✅ Phase 2: Heuristic Loop Detection (COMPLETE)

**Status:** Production-ready

Detects and remediates runaway agent loops using OpenTelemetry spans and simple heuristics.

### What's Implemented:
- **OTLP/HTTP Receiver** — accepts OTel JSON spans at `POST /v1/traces` (no protobuf dependency)
- **SpanStore** — thread-safe in-memory sliding-window store of tool calls
- **LivelockPolicy CRD** — configures detection thresholds and remediation actions
- **Heuristic Engine** — polls policies, counts identical tool calls in rolling windows
- **Detection Trigger** — "same tool called N times in M seconds → livelock detected"
- **Cooldown Mechanism** — 2-minute window prevents duplicate remediation on same pod

### Remediation Actions:
- **EvictPod** — force delete pod (grace period 0), pod restarts automatically
- **SuspendWorkload** — scale owning Deployment to 0 replicas
- **InjectFault** — return HTTP 429 to agent (graceful backoff, fallback to EvictPod if loop persists)
- **Kubernetes Events** — Warning events emitted on detection for alerting/logging

### LivelockPolicy Status:
- `detectionCount` — total loops caught since creation
- `lastDetection` — timestamp of most recent loop
- `lastAffectedPod` — which pod was remediated

### Example Configuration:
```yaml
apiVersion: kovern.io/v1alpha1
kind: LivelockPolicy
metadata:
  name: agent-loop-guard
spec:
  selector:
    matchLabels:
      app: myagent
  detection:
    otel:
      maxSameToolCalls: 3       # threshold
      windowSeconds: 30          # rolling window
  remediation:
    action: InjectFault
    faultConfig:
      httpStatusCode: 429
      duration: "60s"
    fallbackAction: EvictPod    # escalate if loop persists
    emitEvent: true
```

---

## 🔄 Phase 3: Semantic Loop Detection (IN PROGRESS)

**Status:** Design phase / initial implementation pending

Detects loops with variation — cases where the agent is semantically stuck but the tool calls aren't identical.

### What's Planned:
- **Claude-powered Detector** — analyze agent turn history for semantic similarity
- **Configurable Windows** — examine last N turns (default 3, max 10)
- **Similarity Threshold** — trigger remediation when semantic similarity exceeds threshold (default 0.92)
- **Multi-model Support** — Anthropic (Claude) + Gemini backends
- **Optional Cost Control** — disabled by default; opt-in via `spec.detection.semantic.enabled`

### Use Cases:
```
Agent turn 1: search_web("stock price") → ERROR (API down)
Agent turn 2: fetch_data("stock ticker") → ERROR (API down)
Agent turn 3: query_market("stocks today") → ERROR (API down)
              ↓
         Different tools, same intent → Semantic loop
         Claude detects similarity → Remediate
```

### Configuration:
```yaml
detection:
  semantic:
    enabled: true                              # opt-in, requires ANTHROPIC_API_KEY
    provider: Anthropic                        # or Gemini
    windowTurns: 5                             # last 5 turns
    similarityThreshold: "0.92"                # 0-1 scale
    model: "claude-haiku-4-5-20251001"        # configurable model
```

### Architecture:
- **Detector Interface** — pluggable backend (Claude, Gemini, custom)
- **NoOpDetector** — disabled mode (default)
- **CloudeDetector** — calls Claude API to evaluate turn similarity
- **Async Evaluation** — runs outside the tight heuristic loop to avoid latency impact

---

## 🚀 Phase 4: Remediation Escalation & Recovery Hints

**Status:** Planned

Smarter, multi-step remediation with recovery guidance for operators.

### Proposed Features:
- **Graduated Escalation** — InjectFault → SuspendWorkload → EvictPod (configurable sequence)
- **Recovery Hints** — emit suggestions in events/logs when loop detected
  - "Consider adjusting retry logic for this endpoint"
  - "Circuit breaker pattern recommended for this external API"
- **Replay Prevention** — maintain pod history to avoid remediating same failure N times in a row
- **Custom Remediation Webhooks** — operators can define custom remediation logic

### Example:
```yaml
remediation:
  escalation:
    - action: InjectFault
      duration: "30s"
      if: "loopCount < 3"
    - action: SuspendWorkload
      if: "loopCount >= 3 && loopCount < 6"
    - action: EvictPod
      if: "loopCount >= 6"
  recoveryHints:
    - condition: "same_tool_called_repeatedly"
      hint: "Agent stuck retrying same endpoint. Check external service health."
    - condition: "timeout_loop"
      hint: "Increase timeout or implement exponential backoff."
```

---

## 🔮 Phase 5: GitOps Integration & Incident Automation

**Status:** Planned

Automated incident response and coordination with git-based workflows.

### Proposed Features:
- **GitOps Incident Reports** — create commits in a gitops repository when loops detected
  - Automated rollback suggestions
  - Operator runbooks linked in commits
- **Jira/Linear Integration** — auto-create incidents/tickets when threshold exceeded
- **Slack/PagerDuty Alerting** — structured alerts with context and remediation links
- **Canary Validation** — test agent deployments in sandbox environment first
  - Run with budget=0 (quota denied immediately on overspend)
  - Validate loop detection thresholds before production rollout

### Use Case:
```
Loop detected on agent → Create commit to gitops repo
                      → File ticket in Linear
                      → Send PagerDuty alert with runbook link
                      → Operator reviews, approves automated rollback
```

---

## 📋 Phase 6: Multi-Cluster & Federation

**Status:** Planned

Support Kovern across multiple Kubernetes clusters with shared governance.

### Proposed Features:
- **Distributed Ledger** — aggregate budget spend across clusters
- **Global Rate Limits** — budget cap applies across all clusters
- **Cluster-local Remediation** — each cluster remediates its own pods independently
- **Cross-cluster Metrics** — unified dashboard of spend + loop detection across fleet

---

## 🛠️ Current Development Focus

### Immediate Priorities:
1. **Phase 3 Implementation** — finish Claude semantic detector (interface + E2E test)
2. **Test Coverage** — expand integration tests for otelreceiver + remediation packages
3. **Observability** — add Prometheus metrics for budget usage + loop detection rate
4. **Documentation** — operator runbooks, troubleshooting guides

### Testing & Quality:
- ✅ Unit tests for heuristic engine (88% coverage)
- ✅ Integration tests for admission webhook
- ⏳ E2E tests with real Ollama agents (planned)
- ⏳ Load tests for admission webhook throughput

### Roadmap Dependencies:
| Phase | Depends On | Status |
|-------|-----------|--------|
| 1 | None | ✅ Complete |
| 2 | Phase 1 | ✅ Complete |
| 3 | Phase 2 | 🔄 In Progress |
| 4 | Phase 2-3 | 📋 Planned |
| 5 | Phase 4 | 📋 Planned |
| 6 | Phase 1-3 | 📋 Planned |

---

## How to Contribute

### Try Phase 2 in Your Cluster:
```bash
# Deploy Kovern
make local-deploy

# Apply demo with budget + loop detection
kubectl apply -f charts/kovern/examples/demo/

# Watch detection in action
kubectl logs -n kovern-system -l app.kubernetes.io/name=kovern -f
```

### Report Issues:
- Budget enforcement edge cases
- Loop detection false positives/negatives
- Performance concerns (webhook latency, remediation timing)

### Request Features:
- Phase 3 semantic detection feedback
- Custom remediation use cases for Phase 4
- Multi-cluster scenarios for Phase 6

---

## Glossary

- **TokenQuota** — CRD defining per-ServiceAccount financial budget
- **LivelockPolicy** — CRD defining loop detection + remediation rules
- **SpanStore** — in-memory sliding-window cache of OTel tool calls
- **Heuristic Engine** — background loop that evaluates policies against SpanStore
- **Admission Webhook** — intercepts pod creates, enforces budget checks
- **Ledger Cache** — fast in-memory copy of budget state (source of truth is TokenQuota.status)
- **Livelock** — agent stuck retrying same action with same failure
- **OTLP** — OpenTelemetry Protocol (span format)
- **Cooldown** — grace period after remediation to avoid duplicate actions

---

## References

- [Architecture Decision Records](./adr/)
- [Demo Walkthrough](./demo-walkthrough.md)
- [Customer Guide](./customer-guide.md)
- [API Documentation](../api/v1alpha1/)
