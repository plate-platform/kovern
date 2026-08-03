package controller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/plate-platform/kovern/api/v1alpha1"
)

// LivelockPolicyReconciler reconciles LivelockPolicy objects.
// In Phase 1 the reconciler watches for policy changes and logs them.
// The OTel-driven heuristic detection engine is added in Phase 2.
type LivelockPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kovern.io,resources=livelockpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kovern.io,resources=livelockpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch

func (r *LivelockPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	policy := &v1alpha1.LivelockPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	logger.Info("reconciled LivelockPolicy",
		"policy", req.NamespacedName,
		"otelEnabled", policy.Spec.Detection.OTel != nil,
		"semanticEnabled", policy.Spec.Detection.Semantic != nil && policy.Spec.Detection.Semantic.Enabled,
		"remediationAction", policy.Spec.Remediation.Action,
	)

	return ctrl.Result{}, nil
}

func (r *LivelockPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.LivelockPolicy{}).
		Complete(r)
}
