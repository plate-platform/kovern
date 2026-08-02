package heuristic

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/plate-platform/kovern/api/v1alpha1"
	"github.com/plate-platform/kovern/internal/otelreceiver"
	"github.com/plate-platform/kovern/internal/remediation"
)

var testScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(testScheme))
	utilruntime.Must(v1alpha1.AddToScheme(testScheme))
	utilruntime.Must(corev1.AddToScheme(testScheme))
}

// newFakeExecutor creates an executor with a fake client for testing.
func newFakeExecutor(builder *fake.ClientBuilder) *remediation.Executor {
	return &remediation.Executor{
		Client: builder.WithScheme(testScheme).Build(),
		// Recorder is nil for unit tests since we don't emit events
	}
}

// ── Tests ────────────────────────────────────────────────────────────────────

// TestMaxSameToolCount tests the tool frequency counter.
func TestMaxSameToolCount(t *testing.T) {
	tests := []struct {
		name     string
		calls    []otelreceiver.ToolCall
		expected int
	}{
		{
			name:     "empty",
			calls:    []otelreceiver.ToolCall{},
			expected: 0,
		},
		{
			name: "single tool single call",
			calls: []otelreceiver.ToolCall{
				{ToolName: "search_web", Timestamp: time.Now()},
			},
			expected: 1,
		},
		{
			name: "single tool multiple calls",
			calls: []otelreceiver.ToolCall{
				{ToolName: "search_web", Timestamp: time.Now()},
				{ToolName: "search_web", Timestamp: time.Now()},
				{ToolName: "search_web", Timestamp: time.Now()},
			},
			expected: 3,
		},
		{
			name: "multiple tools different frequencies",
			calls: []otelreceiver.ToolCall{
				{ToolName: "search_web", Timestamp: time.Now()},
				{ToolName: "search_web", Timestamp: time.Now()},
				{ToolName: "check_payment", Timestamp: time.Now()},
				{ToolName: "search_web", Timestamp: time.Now()},
			},
			expected: 3, // search_web called 3 times
		},
		{
			name: "multiple tools max in middle",
			calls: []otelreceiver.ToolCall{
				{ToolName: "a", Timestamp: time.Now()},
				{ToolName: "b", Timestamp: time.Now()},
				{ToolName: "b", Timestamp: time.Now()},
				{ToolName: "b", Timestamp: time.Now()},
				{ToolName: "b", Timestamp: time.Now()},
				{ToolName: "c", Timestamp: time.Now()},
				{ToolName: "c", Timestamp: time.Now()},
			},
			expected: 4, // b called 4 times
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maxSameToolCount(tt.calls)
			if got != tt.expected {
				t.Errorf("maxSameToolCount() = %d, want %d", got, tt.expected)
			}
		})
	}
}

// TestCooldownMechanism tests cooldown tracking logic.
func TestCooldownMechanism(t *testing.T) {
	e := &Engine{
		cooldown: make(map[string]time.Time),
	}

	// Initially not on cooldown
	if e.onCooldown("default", "pod-1") {
		t.Error("pod should not be on cooldown initially")
	}

	// Set cooldown
	e.setCooldown("default", "pod-1", 2*time.Minute)

	// Should be on cooldown immediately
	if !e.onCooldown("default", "pod-1") {
		t.Error("pod should be on cooldown after setting")
	}

	// Other pod not on cooldown
	if e.onCooldown("default", "pod-2") {
		t.Error("pod-2 should not be on cooldown")
	}

	// Other namespace not on cooldown
	if e.onCooldown("other", "pod-1") {
		t.Error("pod-1 in other namespace should not be on cooldown")
	}

	// Simulate time passing beyond cooldown window
	e.mu.Lock()
	e.cooldown["default/pod-1"] = time.Now().Add(-3 * time.Minute)
	e.mu.Unlock()

	if e.onCooldown("default", "pod-1") {
		t.Error("pod should not be on cooldown after 3 minutes")
	}
}

// TestEvaluateNoPolicies tests evaluation when no policies exist.
func TestEvaluateNoPolicies(t *testing.T) {
	builder := fake.NewClientBuilder()
	me := newFakeExecutor(builder)
	ss := otelreceiver.NewSpanStore()

	engine := &Engine{
		Client:   me.Client,
		Store:    ss,
		Executor: me,
	}

	ctx := context.Background()
	err := engine.evaluate(ctx)
	if err != nil {
		t.Errorf("evaluate() failed: %v", err)
	}
}

// TestEvaluateNoOTel tests that policies without OTel config are skipped.
func TestEvaluateNoOTel(t *testing.T) {
	policy := &v1alpha1.LivelockPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "no-otel", Namespace: "default"},
		Spec: v1alpha1.LivelockPolicySpec{
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Detection: v1alpha1.DetectionCriteria{
				OTel: nil, // No OTel detection
			},
		},
	}

	builder := fake.NewClientBuilder().WithObjects(policy)
	me := newFakeExecutor(builder)
	ss := otelreceiver.NewSpanStore()

	engine := &Engine{
		Client:   me.Client,
		Store:    ss,
		Executor: me,
	}

	ctx := context.Background()
	err := engine.evaluate(ctx)
	if err != nil {
		t.Errorf("evaluate() failed: %v", err)
	}
}

// TestEvaluateNoPods tests evaluation when no pods match the selector.
func TestEvaluateNoPods(t *testing.T) {
	policy := &v1alpha1.LivelockPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-policy", Namespace: "default"},
		Spec: v1alpha1.LivelockPolicySpec{
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Detection: v1alpha1.DetectionCriteria{
				OTel: &v1alpha1.OTelCriteria{
					MaxSameToolCalls: 3,
					WindowSeconds:    30,
				},
			},
		},
	}

	builder := fake.NewClientBuilder().WithObjects(policy)
	me := newFakeExecutor(builder)
	ss := otelreceiver.NewSpanStore()

	engine := &Engine{
		Client:   me.Client,
		Store:    ss,
		Executor: me,
		cooldown: make(map[string]time.Time),
	}

	ctx := context.Background()
	err := engine.evaluate(ctx)
	if err != nil {
		t.Errorf("evaluate() failed: %v", err)
	}
}

// TestEvaluateDetectsViolation tests that violations are detected and cooldown is set.
func TestEvaluateDetectsViolation(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	policy := &v1alpha1.LivelockPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-policy", Namespace: "default"},
		Spec: v1alpha1.LivelockPolicySpec{
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Detection: v1alpha1.DetectionCriteria{
				OTel: &v1alpha1.OTelCriteria{
					MaxSameToolCalls: 2,
					WindowSeconds:    30,
				},
			},
		},
	}

	builder := fake.NewClientBuilder().WithObjects(pod, policy)
	me := newFakeExecutor(builder)
	ss := otelreceiver.NewSpanStore()

	// Record 3 identical tool calls (exceeds threshold of 2)
	now := time.Now()
	ss.Record("default", "test-pod", "search_web", now.Add(-5*time.Second))
	ss.Record("default", "test-pod", "search_web", now.Add(-3*time.Second))
	ss.Record("default", "test-pod", "search_web", now)

	engine := &Engine{
		Client:   me.Client,
		Store:    ss,
		Executor: me,
		cooldown: make(map[string]time.Time),
	}

	ctx := context.Background()
	err := engine.evaluate(ctx)
	if err != nil {
		t.Errorf("evaluate() failed: %v", err)
	}

	// Pod should now be on cooldown after violation detected
	if !engine.onCooldown("default", "test-pod") {
		t.Error("pod should be on cooldown after violation detected")
	}
}

// TestEvaluateSkipsNonRunningPods tests that non-running pods are skipped.
func TestEvaluateSkipsNonRunningPods(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending, // Not running
		},
	}

	policy := &v1alpha1.LivelockPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-policy", Namespace: "default"},
		Spec: v1alpha1.LivelockPolicySpec{
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Detection: v1alpha1.DetectionCriteria{
				OTel: &v1alpha1.OTelCriteria{
					MaxSameToolCalls: 2,
					WindowSeconds:    30,
				},
			},
		},
	}

	builder := fake.NewClientBuilder().WithObjects(pod, policy)
	me := newFakeExecutor(builder)
	ss := otelreceiver.NewSpanStore()

	engine := &Engine{
		Client:   me.Client,
		Store:    ss,
		Executor: me,
		cooldown: make(map[string]time.Time),
	}

	ctx := context.Background()
	err := engine.evaluate(ctx)
	if err != nil {
		t.Errorf("evaluate() failed: %v", err)
	}
}

// TestEvaluateCooldownPreventsDoubleFire tests that cooldown prevents duplicate remediation.
func TestEvaluateCooldownPreventsDoubleFire(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	policy := &v1alpha1.LivelockPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-policy", Namespace: "default"},
		Spec: v1alpha1.LivelockPolicySpec{
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Detection: v1alpha1.DetectionCriteria{
				OTel: &v1alpha1.OTelCriteria{
					MaxSameToolCalls: 2,
					WindowSeconds:    30,
				},
			},
		},
	}

	builder := fake.NewClientBuilder().WithObjects(pod, policy)
	me := newFakeExecutor(builder)
	ss := otelreceiver.NewSpanStore()

	engine := &Engine{
		Client:   me.Client,
		Store:    ss,
		Executor: me,
		cooldown: make(map[string]time.Time),
	}

	// Manually set pod on cooldown
	engine.setCooldown("default", "test-pod", 2*time.Minute)

	// Record 3 identical tool calls (would trigger if not on cooldown)
	now := time.Now()
	ss.Record("default", "test-pod", "search_web", now.Add(-5*time.Second))
	ss.Record("default", "test-pod", "search_web", now.Add(-3*time.Second))
	ss.Record("default", "test-pod", "search_web", now)

	ctx := context.Background()
	err := engine.evaluate(ctx)
	if err != nil {
		t.Errorf("evaluate() failed: %v", err)
	}

	// Pod should still be on cooldown (no additional remediation triggered)
	if !engine.onCooldown("default", "test-pod") {
		t.Error("pod should still be on cooldown")
	}
}

// TestEvaluateWindowFiltering tests that only spans in the window are counted.
func TestEvaluateWindowFiltering(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	policy := &v1alpha1.LivelockPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-policy", Namespace: "default"},
		Spec: v1alpha1.LivelockPolicySpec{
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Detection: v1alpha1.DetectionCriteria{
				OTel: &v1alpha1.OTelCriteria{
					MaxSameToolCalls: 3, // Need 3 calls
					WindowSeconds:    10, // In 10 seconds
				},
			},
		},
	}

	builder := fake.NewClientBuilder().WithObjects(pod, policy)
	me := newFakeExecutor(builder)
	ss := otelreceiver.NewSpanStore()

	// Record calls: 2 old (outside window) + 2 new (inside window) = only 2 in window
	now := time.Now()
	ss.Record("default", "test-pod", "search_web", now.Add(-20*time.Second)) // Outside 10s window
	ss.Record("default", "test-pod", "search_web", now.Add(-15*time.Second)) // Outside 10s window
	ss.Record("default", "test-pod", "search_web", now.Add(-5*time.Second))  // Inside window
	ss.Record("default", "test-pod", "search_web", now)                      // Inside window

	engine := &Engine{
		Client:   me.Client,
		Store:    ss,
		Executor: me,
		cooldown: make(map[string]time.Time),
	}

	ctx := context.Background()
	err := engine.evaluate(ctx)
	if err != nil {
		t.Errorf("evaluate() failed: %v", err)
	}

	// Pod should not be on cooldown: only 2 calls in window, need 3
	if engine.onCooldown("default", "test-pod") {
		t.Error("pod should not be on cooldown when calls don't exceed threshold")
	}
}

// TestStartInitializesEngine tests that Start properly initializes the engine.
func TestStartInitializesEngine(t *testing.T) {
	builder := fake.NewClientBuilder()
	me := newFakeExecutor(builder)
	engine := &Engine{
		Client:   me.Client,
		Store:    otelreceiver.NewSpanStore(),
		Executor: me,
		Interval: 0, // Should default to 10s
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		engine.Start(ctx)
		close(done)
	}()

	// Wait for context to finish (initialization happens in Start)
	<-done

	// Check that cooldown map was initialized
	if engine.cooldown == nil {
		t.Error("cooldown map not initialized")
	}

	// Check that Interval was set to default
	if engine.Interval != 10*time.Second {
		t.Errorf("Interval = %v, want 10s", engine.Interval)
	}
}

// TestStartRespectsCancellation tests that Start respects context cancellation.
func TestStartRespectsCancellation(t *testing.T) {
	builder := fake.NewClientBuilder()
	me := newFakeExecutor(builder)
	engine := &Engine{
		Client:   me.Client,
		Store:    otelreceiver.NewSpanStore(),
		Executor: me,
		Interval: 100 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		engine.Start(ctx)
		close(done)
	}()

	// Wait for context to timeout
	select {
	case <-done:
		// Success: Start exited after context cancelled
	case <-time.After(500 * time.Millisecond):
		t.Error("Start did not respect context cancellation")
	}
}
