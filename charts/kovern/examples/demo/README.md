# Kovern Demo

Two-scenario demo showing Kovern's admission-level budget enforcement and livelock detection.

## Prerequisites

Kovern and kagent must be installed on the cluster:
```bash
# Kovern (from repo root)
make local-deploy

# kagent
helm repo add kagent https://kagent.dev/helm
helm upgrade --install kagent kagent/kagent -n kagent --create-namespace --wait
```

## Setup

```bash
kubectl apply -f 00-namespace.yaml
kubectl apply -f 01-healthy-agent.yaml
kubectl apply -f 02-looping-agent.yaml
```

Verify both agents are ready:
```bash
kubectl get agents -n kovern-demo
# NAME             TYPE          RUNTIME   READY   ACCEPTED
# billing-agent    Declarative   python    True    True
# research-agent   Declarative   python    True    True

kubectl get tq,llp -n kovern-demo
# NAME                                          STATE    SPENT   BUDGET   RENEWAL   AGE
# tokenquota.kovern.io/billing-agent-quota      Active   0       10       Daily     ...
# tokenquota.kovern.io/research-agent-quota     Active   0       50       Monthly   ...
#
# NAME                                                  DETECTIONS   LAST DETECTION   AGE
# livelockpolicy.kovern.io/billing-agent-loop-guard     0                             ...
# livelockpolicy.kovern.io/research-agent-loop-guard    0                             ...
```

---

## Scenario A — Healthy agent (passes)

`research-agent` has a $50 monthly budget. Budget is Active. Pod creates are allowed.

```bash
# Simulate a pod create — should be ALLOWED
kubectl run test-allowed --image=busybox --restart=Never \
  --overrides='{"spec":{"serviceAccountName":"research-agent"}}' \
  -n kovern-demo -- sleep 5

# pod/test-allowed created  ✓

kubectl delete pod test-allowed -n kovern-demo
```

---

## Scenario B — Budget-exhausted agent (fails)

`billing-agent` has a $10 daily budget. Exhaust it, then try to create a pod.

```bash
# Step 1: Exhaust the budget
kubectl patch tq billing-agent-quota -n kovern-demo \
  --type=merge --subresource=status \
  --patch='{"status":{"spentUSD":"10.50","spentTokens":42000,"runCount":87,"state":"Exceeded"}}'

# Step 2: Verify state is Exceeded
kubectl get tq billing-agent-quota -n kovern-demo
# NAME                   STATE      SPENT     BUDGET   RENEWAL   AGE
# billing-agent-quota    Exceeded   10.50     10       Daily     ...

# Step 3: Try to create a pod — admission webhook denies it
kubectl run test-denied --image=busybox --restart=Never \
  --overrides='{"spec":{"serviceAccountName":"billing-agent"}}' \
  -n kovern-demo -- sleep 5
# Error from server (Forbidden): admission webhook "vpod.kovern.io" denied the request:
# kovern: quota Exceeded for kovern-demo/billing-agent — no new agent runs until renewal

# Step 4: Reset budget (simulate daily renewal)
kubectl patch tq billing-agent-quota -n kovern-demo \
  --type=merge --subresource=status \
  --patch='{"status":{"spentUSD":"0","spentTokens":0,"runCount":0,"state":"Active"}}'

# Step 5: Pod create is allowed again
kubectl run test-renewed --image=busybox --restart=Never \
  --overrides='{"spec":{"serviceAccountName":"billing-agent"}}' \
  -n kovern-demo -- sleep 5
# pod/test-renewed created  ✓

kubectl delete pod test-renewed -n kovern-demo
```

---

## Livelock detection (Phase 2)

The `LivelockPolicy` CRs are deployed and the controller watches them. Actual OTel-driven
detection fires in Phase 2 when the OTLP receiver is wired to the reconciler. For now,
inspect the policies to show the governance configuration:

```bash
# Show healthy agent policy (loose thresholds — won't fire under normal use)
kubectl describe llp research-agent-loop-guard -n kovern-demo

# Show looping agent policy (tight thresholds — fires after 3 identical tool calls in 30s)
kubectl describe llp billing-agent-loop-guard -n kovern-demo
```

---

## Teardown

```bash
kubectl delete namespace kovern-demo
```
