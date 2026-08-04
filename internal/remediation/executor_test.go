package remediation_test

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/plate-platform/kovern/api/v1alpha1"
	"github.com/plate-platform/kovern/internal/remediation"
)

var testScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(testScheme))
	utilruntime.Must(v1alpha1.AddToScheme(testScheme))
	utilruntime.Must(corev1.AddToScheme(testScheme))
}

// newFakeClient registers LivelockPolicy's status subresource — required by
// sigs.k8s.io/controller-runtime's fake client (since ~v0.15) for
// Status().Update() to actually persist against the tracker instead of
// silently no-oping. updateStatus's error is only logged by Remediate, never
// returned, so a missing registration here would fail silently rather than
// as a visible test error — every test in this file goes through this helper.
// Imported under the ctrlclient alias so no test can shadow the package name
// with a local variable called client, which is exactly the bug that motivated
// naming every fake-client variable `c` below instead of `client`.
func newFakeClient(objs ...ctrlclient.Object) ctrlclient.Client {
	return fake.NewClientBuilder().
		WithScheme(testScheme).
		WithStatusSubresource(&v1alpha1.LivelockPolicy{}).
		WithObjects(objs...).
		Build()
}

func testPod(name, namespace string, owners ...metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, OwnerReferences: owners},
	}
}

func testPolicy(namespace, name string, remed v1alpha1.RemediationSpec) *v1alpha1.LivelockPolicy {
	return &v1alpha1.LivelockPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       v1alpha1.LivelockPolicySpec{Remediation: remed},
	}
}

// nextEvent reads the one event FakeRecorder should have buffered, failing
// the test if none arrived.
func nextEvent(t *testing.T, recorder *record.FakeRecorder) string {
	t.Helper()
	select {
	case msg := <-recorder.Events:
		return msg
	default:
		t.Fatal("expected an event to be recorded, got none")
		return ""
	}
}

func TestRemediate_InjectFault_EventReportsFallbackNotConfiguredAction(t *testing.T) {
	pod := testPod("agent-1", "team-payments")
	policy := testPolicy("team-payments", "loop-guard", v1alpha1.RemediationSpec{
		Action:         v1alpha1.RemediationInjectFault,
		FallbackAction: v1alpha1.RemediationEvictPod,
		EmitEvent:      true,
	})

	c := newFakeClient(pod, policy)
	recorder := record.NewFakeRecorder(1)
	e := &remediation.Executor{Client: c, Recorder: recorder}

	if err := e.Remediate(context.Background(), pod, policy); err != nil {
		t.Fatalf("Remediate() error = %v", err)
	}

	msg := nextEvent(t, recorder)
	// InjectFault isn't implemented and always falls through to
	// FallbackAction — the event must say what actually ran (EvictPod), not
	// the configured-but-unapplied action (InjectFault), or an operator
	// reading `kubectl get events` is misled about what happened to their pod.
	if strings.Contains(msg, "InjectFault") {
		t.Errorf("event reports the configured action InjectFault instead of what actually ran: %q", msg)
	}
	if !strings.Contains(msg, string(v1alpha1.RemediationEvictPod)) {
		t.Errorf("expected event to report the applied fallback action EvictPod, got %q", msg)
	}
}

func TestRemediate_SuspendWorkload_NoOwner_EventReportsEvictNotSuspend(t *testing.T) {
	// No ReplicaSet owner reference — suspendWorkload can't find a
	// Deployment to scale, so it degrades to eviction internally. The event
	// must reflect that degradation even though SuspendWorkload was configured.
	pod := testPod("agent-1", "team-payments")
	policy := testPolicy("team-payments", "loop-guard", v1alpha1.RemediationSpec{
		Action:    v1alpha1.RemediationSuspendWorkload,
		EmitEvent: true,
	})

	c := newFakeClient(pod, policy)
	recorder := record.NewFakeRecorder(1)
	e := &remediation.Executor{Client: c, Recorder: recorder}

	if err := e.Remediate(context.Background(), pod, policy); err != nil {
		t.Fatalf("Remediate() error = %v", err)
	}

	msg := nextEvent(t, recorder)
	if strings.Contains(msg, "SuspendWorkload") {
		t.Errorf("event reports SuspendWorkload but the pod has no ReplicaSet owner — it was actually evicted: %q", msg)
	}
	if !strings.Contains(msg, string(v1alpha1.RemediationEvictPod)) {
		t.Errorf("expected event to report the degraded EvictPod action, got %q", msg)
	}

	// And the pod really should be gone.
	key := types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
	err := c.Get(context.Background(), key, &corev1.Pod{})
	if err == nil {
		t.Error("expected pod to be deleted (evicted), but it still exists")
	}
}

func TestRemediate_SuspendWorkload_WithOwnerChain_ScalesDeploymentToZero(t *testing.T) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-deploy", Namespace: "team-payments"},
		Spec:       appsv1.DeploymentSpec{Replicas: int32Ptr(1)},
	}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-deploy-abc123",
			Namespace: "team-payments",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Deployment", Name: "agent-deploy", APIVersion: "apps/v1"},
			},
		},
	}
	pod := testPod("agent-1", "team-payments", metav1.OwnerReference{
		Kind: "ReplicaSet", Name: "agent-deploy-abc123", APIVersion: "apps/v1",
	})
	policy := testPolicy("team-payments", "loop-guard", v1alpha1.RemediationSpec{
		Action:    v1alpha1.RemediationSuspendWorkload,
		EmitEvent: true,
	})

	c := newFakeClient(pod, rs, deploy, policy)
	recorder := record.NewFakeRecorder(1)
	e := &remediation.Executor{Client: c, Recorder: recorder}

	if err := e.Remediate(context.Background(), pod, policy); err != nil {
		t.Fatalf("Remediate() error = %v", err)
	}

	msg := nextEvent(t, recorder)
	if !strings.Contains(msg, string(v1alpha1.RemediationSuspendWorkload)) {
		t.Errorf("expected event to report SuspendWorkload, got %q", msg)
	}

	got := &appsv1.Deployment{}
	deployKey := types.NamespacedName{Namespace: "team-payments", Name: "agent-deploy"}
	if err := c.Get(context.Background(), deployKey, got); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 0 {
		t.Errorf("expected deployment replicas=0, got %v", got.Spec.Replicas)
	}
}

func TestRemediate_UpdatesLivelockPolicyStatus(t *testing.T) {
	pod := testPod("agent-1", "team-payments")
	policy := testPolicy("team-payments", "loop-guard", v1alpha1.RemediationSpec{
		Action: v1alpha1.RemediationEvictPod,
	})

	c := newFakeClient(pod, policy)
	e := &remediation.Executor{Client: c, Recorder: record.NewFakeRecorder(1)}

	if err := e.Remediate(context.Background(), pod, policy); err != nil {
		t.Fatalf("Remediate() error = %v", err)
	}

	got := &v1alpha1.LivelockPolicy{}
	policyKey := types.NamespacedName{Namespace: "team-payments", Name: "loop-guard"}
	if err := c.Get(context.Background(), policyKey, got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if got.Status.DetectionCount != 1 {
		t.Errorf("expected DetectionCount=1, got %d", got.Status.DetectionCount)
	}
	if got.Status.LastAffectedPod != "agent-1" {
		t.Errorf("expected LastAffectedPod=agent-1, got %q", got.Status.LastAffectedPod)
	}
	if got.Status.LastDetection == nil {
		t.Error("expected LastDetection to be set")
	}
}

func int32Ptr(i int32) *int32 { return &i }
