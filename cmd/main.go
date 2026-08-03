// Kovern operator entry point.
//
// Starts the controller manager with:
//   - TokenQuota reconciler (quota ledger sync + renewal)
//   - LivelockPolicy reconciler (loop detection config watcher)
//   - ValidatingAdmissionWebhook for Pod CREATE (quota enforcement)
//   - OTLP/HTTP receiver on :4318 (span ingestion for heuristic detection)
//   - Heuristic engine (evaluates LivelockPolicies against the SpanStore)
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/plate-platform/kovern/api/v1alpha1"
	"github.com/plate-platform/kovern/internal/controller"
	"github.com/plate-platform/kovern/internal/heuristic"
	"github.com/plate-platform/kovern/internal/ledger"
	"github.com/plate-platform/kovern/internal/otelreceiver"
	"github.com/plate-platform/kovern/internal/remediation"
	admissionwebhook "github.com/plate-platform/kovern/internal/webhook"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
		webhookPort          int
		certDir              string
		otlpAddr             string
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Metrics endpoint address")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Health probe endpoint address")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election")
	flag.IntVar(&webhookPort, "webhook-port", 9443, "Admission webhook server port")
	flag.StringVar(&certDir, "cert-dir", "/tmp/k8s-webhook-server/serving-certs", "Directory containing TLS certs (tls.crt, tls.key)")
	flag.StringVar(&otlpAddr, "otlp-addr", ":4318", "OTLP/HTTP receiver listen address (plain HTTP)")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("kovern")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "kovern.io",
	})
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	cache := ledger.New()

	if err = (&controller.TokenQuotaReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Cache:  cache,
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create TokenQuota controller")
		os.Exit(1)
	}

	if err = (&controller.LivelockPolicyReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create LivelockPolicy controller")
		os.Exit(1)
	}

	// Register the Pod admission webhook.
	webhookServer := mgr.GetWebhookServer()
	webhookServer.Register("/validate-v1-pod", &admission.Webhook{
		Handler: &admissionwebhook.Handler{
			Cache:   cache,
			Decoder: admission.NewDecoder(scheme),
		},
	})

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// ── OTLP receiver + heuristic engine ─────────────────────────────────────

	spanStore := otelreceiver.NewSpanStore()

	executor := &remediation.Executor{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorderFor("kovern-livelock"),
	}

	engine := &heuristic.Engine{
		Client:   mgr.GetClient(),
		Store:    spanStore,
		Executor: executor,
		Interval: 10 * time.Second,
	}

	// Start the OTLP/HTTP receiver as a plain HTTP server (no TLS — internal cluster traffic only).
	otlpMux := http.NewServeMux()
	otlpMux.Handle("/v1/traces", &otelreceiver.Handler{Store: spanStore})
	otlpServer := &http.Server{
		Addr:    otlpAddr,
		Handler: otlpMux,
	}

	// Register startup hooks so both the OTLP server and heuristic engine
	// start after the manager's caches are synced and stop when ctx is cancelled.
	if err := mgr.Add(runnableFunc(func(ctx context.Context) error {
		logger.Info("starting OTLP receiver", "addr", otlpAddr)
		go func() {
			if err := otlpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error(err, "OTLP receiver error")
			}
		}()
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return otlpServer.Shutdown(shutCtx)
	})); err != nil {
		logger.Error(err, "unable to register OTLP receiver")
		os.Exit(1)
	}

	if err := mgr.Add(runnableFunc(func(ctx context.Context) error {
		logger.Info("starting heuristic engine", "interval", engine.Interval)
		engine.Start(ctx)
		return nil
	})); err != nil {
		logger.Error(err, "unable to register heuristic engine")
		os.Exit(1)
	}

	logger.Info("starting kovern operator")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// runnableFunc adapts a plain func to the manager.Runnable interface.
type runnableFunc func(ctx context.Context) error

func (f runnableFunc) Start(ctx context.Context) error { return f(ctx) }
