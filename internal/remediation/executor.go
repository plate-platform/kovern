// Package remediation implements the actions taken when a livelock is detected.
//
// Supported actions (matching RemediationAction in the API types):
//
//	EvictPod         — deletes the pod immediately (force, grace period 0)
//	SuspendWorkload  — scales the owning Deployment to 0 replicas
//	InjectFault      — not yet implemented; falls through to the fallback action
//
// After applying an action the executor:
//  1. Emits a Kubernetes Warning Event on the LivelockPolicy
//  2. Increments LivelockPolicy.status.detectionCount
//  3. Records LivelockPolicy.status.lastDetection and lastAffectedPod
package remediation

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/plate-platform/kovern/api/v1alpha1"
)

// Executor applies remediation actions and updates LivelockPolicy status.
type Executor struct {
	Client   client.Client
	Recorder record.EventRecorder
}

// Remediate applies the action configured in policy for the given pod.
// It always updates the policy status and emits an event, even on partial failure.
func (e *Executor) Remediate(ctx context.Context, pod *corev1.Pod, policy *v1alpha1.LivelockPolicy) error {
	logger := ctrl.Log.WithName("remediation")

	action := policy.Spec.Remediation.Action
	// appliedAction is what actually ran — distinct from action whenever
	// InjectFault falls through to the fallback (see below). The Event
	// message must report this, not the configured action, or an operator
	// reading `kubectl get events` sees "applied InjectFault" for a pod that
	// was actually evicted.
	appliedAction := action

	var actionErr error
	switch action {
	case v1alpha1.RemediationEvictPod:
		actionErr = e.evictPod(ctx, pod)

	case v1alpha1.RemediationSuspendWorkload:
		appliedAction, actionErr = e.suspendWorkload(ctx, pod)

	case v1alpha1.RemediationInjectFault:
		// InjectFault requires a sidecar or service mesh — not yet implemented.
		// Apply the configured fallback action instead.
		logger.Info("InjectFault not yet implemented, applying fallback",
			"fallback", policy.Spec.Remediation.FallbackAction,
			"pod", pod.Name,
		)
		if policy.Spec.Remediation.FallbackAction == v1alpha1.RemediationSuspendWorkload {
			appliedAction, actionErr = e.suspendWorkload(ctx, pod)
		} else {
			appliedAction = v1alpha1.RemediationEvictPod
			actionErr = e.evictPod(ctx, pod)
		}

	default:
		appliedAction = v1alpha1.RemediationEvictPod
		actionErr = e.evictPod(ctx, pod)
	}

	// Emit event and update status regardless of action success.
	if policy.Spec.Remediation.EmitEvent {
		e.Recorder.Eventf(policy, corev1.EventTypeWarning, "LivelockDetected",
			"livelock detected on pod %s/%s — applied %s", pod.Namespace, pod.Name, appliedAction)
	}

	if err := e.updateStatus(ctx, policy, pod.Name); err != nil {
		logger.Error(err, "status update failed after remediation", "policy", policy.Name)
	}

	if actionErr != nil {
		return fmt.Errorf("remediation %s failed for %s/%s: %w", action, pod.Namespace, pod.Name, actionErr)
	}
	return nil
}

func (e *Executor) evictPod(ctx context.Context, pod *corev1.Pod) error {
	ctrl.Log.WithName("remediation").Info("evicting pod",
		"pod", pod.Name, "namespace", pod.Namespace)
	return e.Client.Delete(ctx, pod, client.GracePeriodSeconds(0))
}

// suspendWorkload scales the Deployment that owns pod to zero replicas.
// Walk: Pod → ReplicaSet (owner ref) → Deployment (owner ref).
//
// It returns the action actually taken, not just an error: when no
// ReplicaSet/Deployment owner chain exists it degrades to EvictPod instead —
// callers must use this return value (not assume SuspendWorkload happened)
// when reporting what was applied, e.g. in the LivelockDetected Event.
func (e *Executor) suspendWorkload(ctx context.Context, pod *corev1.Pod) (v1alpha1.RemediationAction, error) {
	logger := ctrl.Log.WithName("remediation")

	rsName := ""
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "ReplicaSet" {
			rsName = ref.Name
			break
		}
	}
	if rsName == "" {
		logger.Info("pod has no ReplicaSet owner, falling back to eviction", "pod", pod.Name)
		return v1alpha1.RemediationEvictPod, e.evictPod(ctx, pod)
	}

	rs := &appsv1.ReplicaSet{}
	if err := e.Client.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: rsName}, rs); err != nil {
		return v1alpha1.RemediationSuspendWorkload, fmt.Errorf("get replicaset %s: %w", rsName, err)
	}

	deployName := ""
	for _, ref := range rs.OwnerReferences {
		if ref.Kind == "Deployment" {
			deployName = ref.Name
			break
		}
	}
	if deployName == "" {
		logger.Info("replicaset has no Deployment owner, falling back to eviction",
			"rs", rsName, "pod", pod.Name)
		return v1alpha1.RemediationEvictPod, e.evictPod(ctx, pod)
	}

	deploy := &appsv1.Deployment{}
	if err := e.Client.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: deployName}, deploy); err != nil {
		return v1alpha1.RemediationSuspendWorkload, fmt.Errorf("get deployment %s: %w", deployName, err)
	}

	zero := int32(0)
	deploy.Spec.Replicas = &zero
	logger.Info("scaling deployment to zero", "deployment", deployName, "namespace", pod.Namespace)
	return v1alpha1.RemediationSuspendWorkload, e.Client.Update(ctx, deploy)
}

func (e *Executor) updateStatus(ctx context.Context, policy *v1alpha1.LivelockPolicy, podName string) error {
	now := metav1.Now()
	policy.Status.DetectionCount++
	policy.Status.LastDetection = &now
	policy.Status.LastAffectedPod = podName
	return e.Client.Status().Update(ctx, policy)
}
