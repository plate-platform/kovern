// Package webhook implements the Kovern ValidatingAdmissionWebhook for Pod creation.
//
// The webhook intercepts Pod CREATE operations and checks the quota ledger for the
// pod's service account. Pods are denied when the quota state is Exceeded or Suspended.
// The webhook reads exclusively from the in-memory ledger cache — it never hits the
// Kubernetes API on the hot path.
package webhook

import (
	"context"
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/plate-platform/kovern/api/v1alpha1"
	"github.com/plate-platform/kovern/internal/ledger"
)

// Handler is the ValidatingAdmissionWebhook handler.
// It implements admission.Handler.
type Handler struct {
	Cache   *ledger.Cache
	Decoder admission.Decoder
}

// Handle is called for every Pod CREATE request.
func (h *Handler) Handle(ctx context.Context, req admission.Request) admission.Response {
	logger := log.FromContext(ctx)

	if req.Operation != admissionv1.Create {
		return admission.Allowed("not a create operation")
	}

	pod := &corev1.Pod{}
	if err := h.Decoder.DecodeRaw(req.Object, pod); err != nil {
		logger.Error(err, "failed to decode pod")
		return admission.Errored(http.StatusBadRequest, err)
	}

	sa := pod.Spec.ServiceAccountName
	if sa == "" {
		sa = "default"
	}
	ns := req.Namespace

	state, err := h.Cache.Check(ns, sa)
	if err != nil {
		// Fail-open: cache errors must never block legitimate workloads.
		logger.Error(err, "quota check error — failing open", "namespace", ns, "serviceAccount", sa)
		return admission.Allowed("quota cache error — failing open")
	}

	switch state {
	case v1alpha1.QuotaStateExceeded, v1alpha1.QuotaStateSuspended:
		logger.Info("pod denied — quota exceeded",
			"namespace", ns, "serviceAccount", sa, "state", state)
		return admission.Denied(fmt.Sprintf(
			"kovern: quota %s for %s/%s — no new agent runs until the next renewal period",
			state, ns, sa,
		))

	case v1alpha1.QuotaStateSoftLimit:
		logger.Info("quota soft limit reached — pod allowed with warning",
			"namespace", ns, "serviceAccount", sa)
		return admission.Allowed("quota active").
			WithWarnings(fmt.Sprintf(
				"kovern: quota soft limit reached for %s/%s — budget is running low",
				ns, sa,
			))

	default:
		return admission.Allowed("quota active")
	}
}
