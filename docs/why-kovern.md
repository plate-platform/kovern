# Kovern: What Problems Does It Solve?

AI agents on Kubernetes have two critical failure modes that silently drain your budget and disrupt service. Kovern prevents both.

## Problem 1: Budget Overruns

**The situation:**
- You budget $50/month for a billing agent
- Agent makes 100 API calls/minute, each costs $0.01
- One bug or external service outage causes runaway retries
- By the time you notice the logs, you've spent $100 — double your budget

**Why it's hard to fix:**
- Budget limits are soft — usually enforced at your billing vendor, not your infrastructure
- You only notice overages in the vendor's bill (hours or days later)
- Token/API usage doesn't map cleanly to cost (different models, different pricing)
- Agents don't know when to stop — they just keep calling

**What Kovern does:**
Enforces a hard budget cap **at the Kubernetes API level**, before any agent pod starts.
- Once a ServiceAccount exhausts its monthly/daily budget, no new agent pods can run
- No more overages — ever
- You control the limit (SoftLimit ⚠️ at 80%, HardLimit ❌ at 100%)
- Works with any agent SDK, language, or framework

---

## Problem 2: Runaway Loops

**The situation:**
- Agent calls `check_payment()` function
- External service is down
- Agent retries the same call 500× over 2 hours
- Your quota burns, the downstream service gets hammered, no one notices until it's too late

**Why it's hard to fix:**
- Retry logic is built into every agent SDK — you can't turn it off
- A single stuck function can spiral into hundreds of wasted calls
- By the time you see the pod in a "CrashLoopBackOff" state, the damage is done
- Timeouts are too blunt — they kill legitimate long-running work, but miss quick tight loops

**What Kovern does:**
Detects identical function calls in a rolling window and stops the pod immediately.
- Same tool called 3× in 30 seconds → **pod evicted in 40 seconds total**
- You get a Kubernetes event + alert (standard kubectl/Prometheus/Datadog integration)
- Pod restarts and tries a different approach
- Minimal wasted resources

---

## Real Impact

| Scenario | Without Kovern | With Kovern |
|---|---|---|
| **E-commerce agent**: Payment API down 15 min | $120 wasted (20% overage) | $100 hard cap enforced |
| **Data pipeline**: Stuck fetch_batch loop | $500 lost to 50K wasted API calls | ~$3 lost to 300 calls before eviction |
| **Research agent**: Combined budget + loop control | No safety net | $20/day hard cap + loop detection in seconds |

---

## The Operators' Burden (Without Kovern)

- **Reactionary:** You respond to costs/errors after they happen
- **Manual:** You manually patch budget limits, kill pods, investigate logs
- **Cross-cutting:** Budget logic lives in your billing vendor (hard to track), loop detection is custom in each agent framework
- **Blind spots:** You don't know which agent burned what budget, which pod looped when

## The Operators' Relief (With Kovern)

- **Proactive:** Budget/loop metrics are visible in real time (kubectl, Prometheus, Datadog)
- **Automatic:** Enforcement happens at the control plane; no SDK changes needed
- **Unified:** All agents on Kubernetes use the same budget + loop detection rules
- **Visible:** Standard Kubernetes events and status conditions; integrates with your existing monitoring

---

## How Kovern Works (Two Independent Controls)

### Control 1: Budget Enforcement (Admission Webhook)

Every time an agent pod tries to **start**, Kubernetes asks Kovern: "Can this run?"

```
Agent wants to start
    ↓
Kubernetes calls ValidatingAdmissionWebhook
    ↓
Kovern checks: "Is this service account within budget?"
    ↓
    ├─→ Budget Active    → Allow ✓
    ├─→ Budget SoftLimit → Allow + Warn ⚠️
    └─→ Budget Exceeded  → Deny ✗ (pod never starts)
```

**Who enforces it?** The Kubernetes API itself — no SDK, framework, or code changes needed.

**Why it matters:** Your agents can be written in any language (Python, Go, Node, Rust). No matter what framework they use, Kovern enforces the budget at the control plane.

### Control 2: Loop Detection (Heuristic Engine)

Every 10 seconds, Kovern checks: "Is any pod calling the same tool repeatedly?"

```
Agent pod emits spans:
  [10:00:00] Call: search_web → TIMEOUT
  [10:00:01] Call: search_web → TIMEOUT
  [10:00:02] Call: search_web → TIMEOUT   ← 3 identical calls in 30 seconds
              ↓
         Threshold crossed → DETECT LIVELOCK
              ↓
         Stop pod (evict immediately)
         Update policy status
         Emit Kubernetes event (alert)
              ↓
    Pod restarts, tries a different approach
```

**Why it matters:** You catch loops **within seconds**, not hours. Minimal wasted resources.

---

## Real Customer Scenarios

### Scenario 1: E-Commerce Support Agent (SOLVES BUDGET OVERRUN)

**The Agent:** Helps customers with refunds. Calls payment API every time.

**The Problem:**
- Payment API goes down for 15 minutes
- Agent doesn't know, keeps retrying silently
- Each call costs $0.02
- Monthly budget: $100
- Actual spend after 15 min: $120 (quota exceeded by $20)

**Before Kovern:**
- You get a Slack alert 30 minutes later: "quota exceeded"
- You manually check logs, find the API was down
- You've already overspent $20
- You have to pay it

**With Kovern:**
```yaml
apiVersion: kovern.io/v1alpha1
kind: TokenQuota
metadata:
  name: support-agent-budget
  namespace: production
spec:
  targetRef:
    kind: ServiceAccount
    name: support-agent
  billingScope:
    maxFinancialBudget: "100"
    renewalInterval: Monthly
    softLimitPct: 80  # alert at $80
  enforcementAction: SuspendWorkload  # scale deployment to 0 when over
```

**What happens:**
1. Agent hits exactly $80 spent → Kubernetes Warning Event fired → PagerDuty alert
2. You have 5 minutes to investigate (still $20 buffer)
3. Agent hits exactly $100 spent → **No more pods can start** → hard stop
4. You manually patch the quota or scale the agent to 0
5. You pay exactly $100, no overages

---

### Scenario 2: Data Pipeline Agent (SOLVES LOOP DETECTION)

**The Agent:** Extracts data from a partner API. Runs hourly.

**The Problem:**
```
Normal run: 5 minutes, 100 API calls, process 10,000 rows
Stuck run:  60 minutes, 50,000 API calls, process 0 rows
```

The agent gets stuck in a retry loop on a specific API endpoint. It's calling the same `fetch_batch` function 500× with the same error each time.

**Before Kovern:**
- Agent runs to completion (1 hour of retries)
- You get billed for 50,000 API calls ($500)
- Your quota alert triggers, but it's too late
- You manually kill the pod
- The data isn't processed, your SLA misses

**With Kovern:**
```yaml
apiVersion: kovern.io/v1alpha1
kind: LivelockPolicy
metadata:
  name: pipeline-loop-guard
  namespace: production
spec:
  selector:
    matchLabels:
      app: data-pipeline-agent
  detection:
    otel:
      maxSameToolCalls: 3       # same tool 3× in window → loop
      windowSeconds: 30         # 30-second rolling window
      identicalResponseHash: true
  remediation:
    action: EvictPod            # kill the stuck pod
    fallbackAction: EvictPod
    emitEvent: true             # Kubernetes Warning Event + alert
```

**What happens:**
1. Agent calls `fetch_batch` → ERROR
2. Agent calls `fetch_batch` again → ERROR (same error)
3. Agent calls `fetch_batch` again → ERROR (same error)
4. Kovern detects 3 identical calls in 30 seconds
5. **Pod is evicted immediately** (within 40 seconds total)
6. Kubernetes event fires: "livelock detected on agent — evicted"
7. PagerDuty alert: "Data pipeline agent evicted due to loop"
8. You investigate during your next check (not in a panic 1 hour later)
9. You roll back the agent code or disable that endpoint
10. Agent restarts, processes the remaining data

**Cost impact:**
- Without Kovern: 50,000 wasted API calls = $500 loss
- With Kovern: ~300 API calls before eviction = ~$3 loss

---

### Scenario 3: Research Agent (BUDGET + LOOP DETECTION TOGETHER)

**The Agent:** Researches investment opportunities. Uses web search + document analysis.

**Setup:**
```yaml
# Budget: $20/day
apiVersion: kovern.io/v1alpha1
kind: TokenQuota
metadata:
  name: research-agent-budget
spec:
  targetRef:
    kind: ServiceAccount
    name: research-agent
  billingScope:
    maxFinancialBudget: "20"
    renewalInterval: Daily
    softLimitPct: 80

---
# Loop detection: same search 3× in 1 minute = livelock
apiVersion: kovern.io/v1alpha1
kind: LivelockPolicy
metadata:
  name: research-agent-loop-guard
spec:
  selector:
    matchLabels:
      app: research-agent
  detection:
    otel:
      maxSameToolCalls: 3
      windowSeconds: 60
  remediation:
    action: InjectFault    # return HTTP 429 (graceful backoff)
    faultConfig:
      httpStatusCode: 429
      duration: "60s"
    fallbackAction: EvictPod
```

**What happens on a bad day:**
- 10:00 AM: Agent is running fine, $2 spent
- 10:30 AM: Agent gets stuck in a web search loop (search engine rate-limited)
  - Loop detected after 1 minute
  - Injected HTTP 429 (graceful backoff)
  - Agent pauses for 60 seconds, tries different query
  - Agent recovers, continues
- 02:00 PM: Agent hits $16 spent (80% threshold)
  - Kubernetes Warning Event fired
  - PagerDuty alert: "Research agent at soft limit"
  - You check that costs are normal, no action needed
- 05:00 PM: Agent would hit $20 exactly
  - **No more pods can start**
  - Any scheduled research runs are queued until tomorrow's renewal
  - You stay within budget

---

## Key Benefits Summary

| Benefit | How Kovern Delivers |
|---------|-------------------|
| **Cost Control** | Hard cap on spend — no overages, ever |
| **Fast Detection** | Catches loops in seconds, not hours |
| **No Code Changes** | Works with any agent SDK, language, framework |
| **Admission-Level** | Enforced at Kubernetes API, before pod starts |
| **Transparent** | Standard Kubernetes events, logs, CRDs — no custom tooling |
| **Fallback Actions** | Graceful backoff (429) → escalate to pod eviction if needed |
| **Status Visibility** | Policy status shows detection count, affected pods, last timestamp |

---

## What Gets Monitored?

### Budget Metrics (TokenQuota Status)

```
kubectl get tq -A

NAME                    STATE      SPENT    BUDGET   RENEWAL   AGE
research-agent-quota    SoftLimit  16.00    20       Daily     3h
support-agent-quota     Active     45.23    100      Monthly   12d
pipeline-agent-quota    Exceeded   250.15   250      Daily     1d
```

Each quota shows:
- **STATE**: Active | SoftLimit (80%) | Exceeded | Suspended
- **SPENT**: Total cost recorded this period
- **BUDGET**: Hard cap per renewal period
- **RENEWAL**: When the counter resets (Hourly/Daily/Monthly)

### Loop Detection Metrics (LivelockPolicy Status)

```
kubectl get llp -A

NAME                          DETECTIONS   LAST DETECTION   SEMANTIC
support-agent-loop-guard      2            2 minutes ago    false
pipeline-loop-guard           18           30 seconds ago   false
research-agent-loop-guard     0            —                false
```

Each policy shows:
- **DETECTIONS**: Total loops caught since creation
- **LAST DETECTION**: When the most recent loop was found
- **AFFECTED POD**: Which pod was remediated
- **SEMANTIC**: Whether LLM-based detection is enabled (future)

---

## Integration with Your Monitoring

All enforcement actions are visible in your existing tools:

```bash
# Kubernetes events (standard kubectl, Datadog, Prometheus)
kubectl describe llp research-agent-loop-guard
# Events:
#   Type     Reason             Message
#   ----     ------             -------
#   Warning  LivelockDetected   livelock detected on pod research-agent-... — applied EvictPod

# Metrics (Prometheus scrape targets)
# kovern_livelockpolicy_detections_total
# kovern_tokenquota_state (Active=0, SoftLimit=1, Exceeded=2)
# kovern_tokenquota_spent_usd

# Logs (kubectl logs, ELK, Datadog, Splunk)
# "livelock detected" → grep operator logs
# "quota Exceeded" → admission webhook logs
```

---

## Getting Started

**Step 1: Define budgets**
```yaml
# One TokenQuota per agent or service account
# Set the monthly/daily limit and soft-limit threshold
kubectl apply -f quotas.yaml
```

**Step 2: Define loop policies**
```yaml
# One LivelockPolicy per agent type or namespace
# Set detection thresholds (same tool N× in M seconds)
kubectl apply -f policies.yaml
```

**Step 3: Deploy agents normally**
```bash
# No code changes needed — agents work exactly as before
# Kovern enforces from the control plane
kubectl apply -f agent.yaml
```

**Step 4: Monitor**
```bash
# Watch budget and detection metrics
kubectl get tq,llp -n your-namespace -w
```

---

## FAQ

**Q: Will my agent code break?**
A: No. Kovern operates at the Kubernetes level. Your agent code, SDK, and framework are unchanged.

**Q: What if I need to temporarily go over budget?**
A: Patch the TokenQuota to increase the limit or mark it Active. Changes take effect within seconds.

**Q: Can Kovern prevent legitimate agent retries?**
A: Only if the retries are **identical** (same tool, same error, same response). Agents that call different tools or get different results won't trigger.

**Q: What about semantic loops (different errors but same intent)?**
A: Phase 2 uses heuristic detection (identical tool calls). Phase 3 will add Claude-powered semantic detection to catch "loops with variation."

**Q: How does this compare to timeout-based limits?**
A: Timeouts kill the agent after N minutes. Kovern kills it after **N identical failures in a rolling window** — much faster for stuck loops, much gentler for long-running jobs that legitimately take time.

