// Package integration_test contains integration tests that run against a real
// Kubernetes API server (via envtest) with the Kovern controllers active.
// No real cluster is required; no webhook TLS is needed.
//
// Run with: make test-integration
package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1alpha1 "github.com/plate-platform/kovern/api/v1alpha1"
	"github.com/plate-platform/kovern/internal/controller"
	"github.com/plate-platform/kovern/internal/ledger"
)

var (
	testEnv   *envtest.Environment
	k8sClient client.Client
	testCache *ledger.Cache
	ctx       context.Context
	cancel    context.CancelFunc
)

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		// Allow plain `go test` to skip gracefully without the envtest binaries.
		// Run `make test-integration` to get the correct assets injected.
		os.Stderr.WriteString("KUBEBUILDER_ASSETS not set — skipping integration tests (run `make test-integration`)\n")
		os.Exit(0)
	}

	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	ctx, cancel = context.WithCancel(context.Background())

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "testdata")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	if err != nil {
		panic("envtest start: " + err.Error())
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		panic("k8s client: " + err.Error())
	}

	testCache = ledger.New()

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	if err != nil {
		panic("manager: " + err.Error())
	}

	if err := (&controller.TokenQuotaReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Cache:  testCache,
	}).SetupWithManager(mgr); err != nil {
		panic("TokenQuotaReconciler setup: " + err.Error())
	}

	if err := (&controller.LivelockPolicyReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		panic("LivelockPolicyReconciler setup: " + err.Error())
	}

	go func() {
		if err := mgr.Start(ctx); err != nil && ctx.Err() == nil {
			panic("manager run: " + err.Error())
		}
	}()

	code := m.Run()

	cancel()
	_ = testEnv.Stop()
	os.Exit(code)
}

// poll calls check every interval until it returns true or timeout expires.
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
