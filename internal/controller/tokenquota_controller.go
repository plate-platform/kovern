// Package controller contains the Kovern controller reconcilers.
package controller

import (
	"context"
	"fmt"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/plate-platform/kovern/api/v1alpha1"
	"github.com/plate-platform/kovern/internal/ledger"
)

// TokenQuotaReconciler reconciles TokenQuota objects and keeps the in-memory
// quota ledger in sync. It also drives the renewal cycle and status reporting.
type TokenQuotaReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Cache  *ledger.Cache
}

// +kubebuilder:rbac:groups=kovern.io,resources=tokenquotas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kovern.io,resources=tokenquotas/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kovern.io,resources=tokenquotas/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *TokenQuotaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	tq := &v1alpha1.TokenQuota{}
	if err := r.Get(ctx, req.NamespacedName, tq); err != nil {
		if apierrors.IsNotFound(err) {
			r.Cache.Delete(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Sync ledger from the authoritative status.
	r.Cache.Sync(tq)

	// Drive renewal: if NextRenewal has passed, reset the ledger and recompute.
	if tq.Status.NextRenewal != nil && time.Now().After(tq.Status.NextRenewal.Time) {
		logger.Info("renewal period elapsed — resetting quota", "quota", req.NamespacedName)
		r.Cache.Reset(req.Namespace, tq.Spec.TargetRef.Name)

		next := nextRenewal(time.Now(), tq.Spec.BillingScope.RenewalInterval)
		tq.Status.SpentUSD = "0"
		tq.Status.SpentTokens = 0
		tq.Status.RunCount = 0
		tq.Status.State = v1alpha1.QuotaStateActive
		tq.Status.NextRenewal = &metav1.Time{Time: next}

		if err := r.Status().Update(ctx, tq); err != nil {
			return ctrl.Result{}, fmt.Errorf("reset status update: %w", err)
		}
		return ctrl.Result{RequeueAfter: time.Until(next)}, nil
	}

	// Set initial NextRenewal if missing.
	if tq.Status.NextRenewal == nil {
		next := nextRenewal(time.Now(), tq.Spec.BillingScope.RenewalInterval)
		tq.Status.NextRenewal = &metav1.Time{Time: next}
		if tq.Status.State == "" {
			tq.Status.State = v1alpha1.QuotaStateActive
		}
		if err := r.Status().Update(ctx, tq); err != nil {
			return ctrl.Result{}, fmt.Errorf("initial status update: %w", err)
		}
	}

	// Reconcile status fields from in-memory cache.
	entry := r.Cache.Get(req.Namespace, tq.Spec.TargetRef.Name)
	if entry != nil {
		tq.Status.SpentUSD = strconv.FormatFloat(entry.SpentUSD, 'f', 6, 64)
		tq.Status.SpentTokens = entry.SpentTokens
		tq.Status.RunCount = entry.RunCount
		tq.Status.State = entry.State
		now := metav1.Now()
		tq.Status.LastUpdated = &now

		if err := r.Status().Update(ctx, tq); err != nil && !apierrors.IsConflict(err) {
			return ctrl.Result{}, fmt.Errorf("status update: %w", err)
		}
	}

	requeueAfter := 5 * time.Minute
	if tq.Status.NextRenewal != nil {
		remaining := time.Until(tq.Status.NextRenewal.Time)
		if remaining > 0 && remaining < requeueAfter {
			requeueAfter = remaining
		}
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func (r *TokenQuotaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.TokenQuota{}).
		Complete(r)
}

// nextRenewal computes the next renewal timestamp from the current time.
func nextRenewal(now time.Time, interval v1alpha1.RenewalInterval) time.Time {
	switch interval {
	case v1alpha1.RenewalHourly:
		return now.Add(time.Hour)
	case v1alpha1.RenewalDaily:
		return now.Add(24 * time.Hour)
	default: // Monthly
		return now.AddDate(0, 1, 0)
	}
}
