// Package e2e_test contains end-to-end tests for the Kovern admission webhook.
// Tests run against the current kubeconfig context and require a live Kovern
// deployment in kovern-system.
//
// Run with: make e2e
package e2e_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	v1alpha1 "github.com/plate-platform/kovern/api/v1alpha1"
)

const (
	e2eNamespace = "kovern-e2e"
	testTimeout  = 30 * time.Second
	pollInterval = 400 * time.Millisecond
)

var (
	k8sClient client.Client
	ctx       context.Context
)

func TestMain(m *testing.M) {
	ctx = context.Background()

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))

	cfg, err := config.GetConfig()
	if err != nil {
		fmt.Printf("no cluster available, skipping e2e: %v\n", err)
		os.Exit(0)
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Printf("failed to build k8s client: %v\n", err)
		os.Exit(1)
	}

	// Require kovern-system namespace — proves Kovern is deployed.
	koverNS := &corev1.Namespace{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "kovern-system"}, koverNS); err != nil {
		fmt.Println("kovern-system namespace not found — deploy Kovern before running e2e (make local-deploy)")
		os.Exit(0)
	}

	// Create the isolated e2e namespace.
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   e2eNamespace,
			Labels: map[string]string{"kovern.io/e2e": "true"},
		},
	}
	if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		fmt.Printf("failed to create e2e namespace: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	_ = k8sClient.DeleteAllOf(ctx, &v1alpha1.TokenQuota{}, client.InNamespace(e2eNamespace))
	_ = k8sClient.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(e2eNamespace))
	_ = k8sClient.Delete(ctx, ns)

	os.Exit(code)
}

// poll retries check every interval until it returns true or timeout expires.
func poll(t *testing.T, timeout, interval time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(interval)
	}
	t.Fatalf("condition not met after %s", timeout)
}

// makePod returns a minimal pod spec running as the given ServiceAccount.
func makePod(name, namespace, saName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: saName,
			RestartPolicy:      corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:            "test",
				Image:           "gcr.io/distroless/static:nonroot",
				ImagePullPolicy: corev1.PullIfNotPresent,
			}},
		},
	}
}

// ensureSA creates a ServiceAccount if it doesn't already exist.
func ensureSA(t *testing.T, name, namespace string) {
	t.Helper()
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	if err := k8sClient.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create SA %s: %v", name, err)
	}
}

// cleanupPod deletes a pod by name, ignoring not-found.
func cleanupPod(name, namespace string) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	_ = k8sClient.Delete(ctx, pod)
}
