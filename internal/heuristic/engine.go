// Package heuristic implements the OTel span-based livelock detection engine.
//
// The Engine runs on a configurable ticker (default 10 s), lists all
// LivelockPolicies across all namespaces, finds pods that match each policy's
// selector, and asks the SpanStore whether any pod has crossed the
// maxSameToolCalls threshold within the rolling windowSeconds.
//
// When a violation is found the Engine calls the Executor to apply the
// configured remediation and puts the pod on a cooldown to avoid
// double-remediation within the same window.
package heuristic

import (
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/plate-platform/kovern/api/v1alpha1"
	"github.com/plate-platform/kovern/internal/otelreceiver"
	"github.com/plate-platform/kovern/internal/remediation"
)

// Engine polls the SpanStore and fires remediations when loop thresholds are crossed.
type Engine struct {
	Client   client.Client
	Store    *otelreceiver.SpanStore
	Executor *remediation.Executor

	// Interval between evaluation ticks. Defaults to 10 s.
	Interval time.Duration

	mu       sync.Mutex
	cooldown map[string]time.Time // "namespace/pod" → last remediation time
}

// Start runs the detection loop until ctx is cancelled. Intended to be called
// in a goroutine: go engine.Start(ctx).
func (e *Engine) Start(ctx context.Context) {
	if e.Interval == 0 {
		e.Interval = 10 * time.Second
	}
	e.cooldown = make(map[string]time.Time)

	ticker := time.NewTicker(e.Interval)
	defer ticker.Stop()

	logger := ctrl.Log.WithName("heuristic-engine")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Trim store to prevent unbounded growth (keep last 10 min).
			e.Store.Trim(10 * time.Minute)
			if err := e.evaluate(ctx); err != nil {
				logger.Error(err, "evaluation tick failed")
			}
		}
	}
}

func (e *Engine) evaluate(ctx context.Context) error {
	logger := ctrl.Log.WithName("heuristic-engine")

	policies := &v1alpha1.LivelockPolicyList{}
	if err := e.Client.List(ctx, policies); err != nil {
		return err
	}

	for i := range policies.Items {
		policy := &policies.Items[i]
		if policy.Spec.Detection.OTel == nil {
			continue
		}
		otel := policy.Spec.Detection.OTel

		sel, err := metav1.LabelSelectorAsSelector(&policy.Spec.Selector)
		if err != nil {
			logger.Error(err, "invalid selector", "policy", policy.Name)
			continue
		}

		pods := &corev1.PodList{}
		if err := e.Client.List(ctx, pods,
			client.InNamespace(policy.Namespace),
			client.MatchingLabelsSelector{Selector: sel},
		); err != nil {
			logger.Error(err, "listing pods", "policy", policy.Name)
			continue
		}

		for j := range pods.Items {
			pod := &pods.Items[j]
			if pod.Status.Phase != corev1.PodRunning {
				continue
			}
			if e.onCooldown(pod.Namespace, pod.Name) {
				continue
			}

			calls := e.Store.Window(pod.Namespace, pod.Name, otel.WindowSeconds)
			if maxSameToolCount(calls) >= otel.MaxSameToolCalls {
				logger.Info("livelock detected",
					"pod", pod.Name,
					"namespace", pod.Namespace,
					"policy", policy.Name,
					"calls", len(calls),
				)
				cooldownDur := time.Duration(otel.WindowSeconds) * 2 * time.Second
				e.setCooldown(pod.Namespace, pod.Name, cooldownDur)

				if err := e.Executor.Remediate(ctx, pod, policy); err != nil {
					logger.Error(err, "remediation failed", "pod", pod.Name)
				}
			}
		}
	}
	return nil
}

// maxSameToolCount returns the highest call count for any single tool in calls.
func maxSameToolCount(calls []otelreceiver.ToolCall) int {
	counts := make(map[string]int, len(calls))
	for _, c := range calls {
		counts[c.ToolName]++
	}
	max := 0
	for _, n := range counts {
		if n > max {
			max = n
		}
	}
	return max
}

func (e *Engine) onCooldown(namespace, pod string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	last, ok := e.cooldown[namespace+"/"+pod]
	return ok && time.Since(last) < 2*time.Minute
}

func (e *Engine) setCooldown(namespace, pod string, d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cooldown[namespace+"/"+pod] = time.Now()
	_ = d // duration is handled via onCooldown using a fixed 2-minute window
}

