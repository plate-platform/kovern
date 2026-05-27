package webhook_test

import (
	"context"
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/plate-platform/kovern/api/v1alpha1"
	"github.com/plate-platform/kovern/internal/ledger"
	"github.com/plate-platform/kovern/internal/webhook"
	"k8s.io/apimachinery/pkg/api/resource"
)

var (
	testScheme = runtime.NewScheme()
	testCodecs serializer.CodecFactory
)

func init() {
	_ = corev1.AddToScheme(testScheme)
	testCodecs = serializer.NewCodecFactory(testScheme)
}

func makeDecoder() admission.Decoder {
	return admission.NewDecoder(testScheme)
}

func encodePod(pod *corev1.Pod) []byte {
	data, err := json.Marshal(pod)
	if err != nil {
		panic(err)
	}
	return data
}

func makePod(name, ns, sa string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: sa,
			Containers: []corev1.Container{
				{Name: "agent", Image: "agent:latest"},
			},
		},
	}
}

func makeRequest(pod *corev1.Pod, op admissionv1.Operation) admission.Request {
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:       "test-uid",
			Operation: op,
			Namespace: pod.Namespace,
			Object: runtime.RawExtension{
				Raw: encodePod(pod),
			},
		},
	}
}

func syncQuota(c *ledger.Cache, ns, sa, maxUSD string, state v1alpha1.QuotaState) {
	tq := &v1alpha1.TokenQuota{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns},
		Spec: v1alpha1.TokenQuotaSpec{
			TargetRef:    v1alpha1.TargetRef{Kind: "ServiceAccount", Name: sa},
			BillingScope: v1alpha1.BillingScope{MaxFinancialBudget: resource.MustParse(maxUSD), SoftLimitPct: 80},
		},
	}
	tq.Status.State = state
	c.Sync(tq)
}

func TestHandle_NonCreate_Allowed(t *testing.T) {
	c := ledger.New()
	h := &webhook.Handler{Cache: c, Decoder: makeDecoder()}
	pod := makePod("pod-1", "team-a", "agent")

	req := makeRequest(pod, admissionv1.Update)
	resp := h.Handle(context.Background(), req)

	if !resp.Allowed {
		t.Fatalf("expected allowed for non-create, got denied: %s", resp.Result.Message)
	}
}

func TestHandle_NoQuota_FailOpen(t *testing.T) {
	c := ledger.New() // empty cache
	h := &webhook.Handler{Cache: c, Decoder: makeDecoder()}
	pod := makePod("pod-1", "team-a", "agent")

	req := makeRequest(pod, admissionv1.Create)
	resp := h.Handle(context.Background(), req)

	if !resp.Allowed {
		t.Fatalf("expected fail-open (allowed) when no quota configured, got denied")
	}
}

func TestHandle_ActiveQuota_Allowed(t *testing.T) {
	c := ledger.New()
	syncQuota(c, "team-a", "agent", "100", v1alpha1.QuotaStateActive)

	h := &webhook.Handler{Cache: c, Decoder: makeDecoder()}
	pod := makePod("pod-1", "team-a", "agent")

	resp := h.Handle(context.Background(), makeRequest(pod, admissionv1.Create))
	if !resp.Allowed {
		t.Fatalf("expected allowed for active quota, got denied: %s", resp.Result.Message)
	}
}

func TestHandle_ExceededQuota_Denied(t *testing.T) {
	c := ledger.New()
	syncQuota(c, "team-a", "agent", "100", v1alpha1.QuotaStateExceeded)

	h := &webhook.Handler{Cache: c, Decoder: makeDecoder()}
	pod := makePod("pod-1", "team-a", "agent")

	resp := h.Handle(context.Background(), makeRequest(pod, admissionv1.Create))
	if resp.Allowed {
		t.Fatal("expected denied for exceeded quota, got allowed")
	}
}

func TestHandle_SuspendedQuota_Denied(t *testing.T) {
	c := ledger.New()
	syncQuota(c, "team-a", "agent", "100", v1alpha1.QuotaStateSuspended)

	h := &webhook.Handler{Cache: c, Decoder: makeDecoder()}
	pod := makePod("pod-1", "team-a", "agent")

	resp := h.Handle(context.Background(), makeRequest(pod, admissionv1.Create))
	if resp.Allowed {
		t.Fatal("expected denied for suspended quota, got allowed")
	}
}

func TestHandle_SoftLimit_AllowedWithWarning(t *testing.T) {
	c := ledger.New()
	syncQuota(c, "team-a", "agent", "100", v1alpha1.QuotaStateSoftLimit)

	h := &webhook.Handler{Cache: c, Decoder: makeDecoder()}
	pod := makePod("pod-1", "team-a", "agent")

	resp := h.Handle(context.Background(), makeRequest(pod, admissionv1.Create))
	if !resp.Allowed {
		t.Fatalf("expected allowed for soft limit, got denied: %s", resp.Result.Message)
	}
	if len(resp.Warnings) == 0 {
		t.Fatal("expected warning for soft limit, got none")
	}
}

func TestHandle_EmptyServiceAccount_DefaultSA(t *testing.T) {
	c := ledger.New()
	syncQuota(c, "team-a", "default", "100", v1alpha1.QuotaStateExceeded)

	h := &webhook.Handler{Cache: c, Decoder: makeDecoder()}
	// pod with no SA — should fall back to "default"
	pod := makePod("pod-1", "team-a", "")

	resp := h.Handle(context.Background(), makeRequest(pod, admissionv1.Create))
	if resp.Allowed {
		t.Fatal("expected denied when 'default' SA quota is exceeded")
	}
}

func TestHandle_DifferentNamespaces_Isolated(t *testing.T) {
	c := ledger.New()
	syncQuota(c, "team-a", "agent", "100", v1alpha1.QuotaStateExceeded)
	syncQuota(c, "team-b", "agent", "100", v1alpha1.QuotaStateActive)

	h := &webhook.Handler{Cache: c, Decoder: makeDecoder()}

	respA := h.Handle(context.Background(), makeRequest(makePod("pod-1", "team-a", "agent"), admissionv1.Create))
	respB := h.Handle(context.Background(), makeRequest(makePod("pod-2", "team-b", "agent"), admissionv1.Create))

	if respA.Allowed {
		t.Fatal("team-a: expected denied for exceeded quota")
	}
	if !respB.Allowed {
		t.Fatal("team-b: expected allowed for active quota")
	}
}
