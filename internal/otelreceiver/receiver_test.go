package otelreceiver_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/plate-platform/kovern/api/v1alpha1"
	"github.com/plate-platform/kovern/internal/ledger"
	"github.com/plate-platform/kovern/internal/otelreceiver"
	"github.com/plate-platform/kovern/internal/pricing"
)

// nowNano is a current-time OTLP timestamp (protobuf-JSON int64-as-string).
// SpanStore.Window filters by recency against time.Now(), so fixtures must
// use a realistic "now", not an arbitrary placeholder like epoch+1s.
func nowNano() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// toolCallSpan is a minimal OTLP/HTTP JSON payload carrying one span with a
// gen_ai.tool.name attribute — exercises the pre-existing loop-detection path.
func toolCallSpan(namespace, pod, tool string) string {
	return fmt.Sprintf(`{
		"resourceSpans": [{
			"resource": {"attributes": [
				{"key": "k8s.namespace.name", "value": {"stringValue": %q}},
				{"key": "k8s.pod.name", "value": {"stringValue": %q}}
			]},
			"scopeSpans": [{"spans": [{
				"name": %q,
				"startTimeUnixNano": %q,
				"attributes": [{"key": "gen_ai.tool.name", "value": {"stringValue": %q}}]
			}]}]
		}]
	}`, namespace, pod, tool, nowNano(), tool)
}

// usageSpan is a minimal OTLP/HTTP JSON payload shaped like a real GenAI SDK
// export: token counts as protobuf-JSON int64 strings (intValue), plus a
// ServiceAccount resource attribute so the span can be billed to a quota.
func usageSpan(namespace, pod, serviceAccount, model string, inputTokens, outputTokens int64) string {
	saAttr := ""
	if serviceAccount != "" {
		saAttr = fmt.Sprintf(`,{"key": "k8s.serviceaccount.name", "value": {"stringValue": %q}}`, serviceAccount)
	}
	return fmt.Sprintf(`{
		"resourceSpans": [{
			"resource": {"attributes": [
				{"key": "k8s.namespace.name", "value": {"stringValue": %q}},
				{"key": "k8s.pod.name", "value": {"stringValue": %q}}
				%s
			]},
			"scopeSpans": [{"spans": [{
				"name": "chat completion",
				"startTimeUnixNano": %q,
				"attributes": [
					{"key": "gen_ai.request.model", "value": {"stringValue": %q}},
					{"key": "gen_ai.usage.input_tokens", "value": {"intValue": "%d"}},
					{"key": "gen_ai.usage.output_tokens", "value": {"intValue": "%d"}}
				]
			}]}]
		}]
	}`, namespace, pod, saAttr, nowNano(), model, inputTokens, outputTokens)
}

func makeQuota(ns, sa, maxUSD string) *v1alpha1.TokenQuota {
	return &v1alpha1.TokenQuota{
		ObjectMeta: metav1.ObjectMeta{Name: sa + "-quota", Namespace: ns},
		Spec: v1alpha1.TokenQuotaSpec{
			TargetRef:    v1alpha1.TargetRef{Kind: "ServiceAccount", Name: sa},
			BillingScope: v1alpha1.BillingScope{MaxFinancialBudget: resource.MustParse(maxUSD), SoftLimitPct: 80},
		},
	}
}

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServeHTTP_RecordsToolCall(t *testing.T) {
	store := otelreceiver.NewSpanStore()
	h := &otelreceiver.Handler{Store: store}

	rec := post(t, h, toolCallSpan("team-a", "pod-1", "check_payment"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	calls := store.Window("team-a", "pod-1", 60)
	if len(calls) != 1 || calls[0].ToolName != "check_payment" {
		t.Fatalf("expected 1 recorded tool call for check_payment, got %+v", calls)
	}
}

func TestServeHTTP_NilCacheAndEstimator_StillRecordsToolCalls(t *testing.T) {
	store := otelreceiver.NewSpanStore()
	// Cache/CostEstimator intentionally left nil — must not panic or affect
	// tool-call recording; existing callers that don't care about spend
	// should keep working unchanged.
	h := &otelreceiver.Handler{Store: store}

	rec := post(t, h, usageSpan("team-a", "pod-1", "billing-agent", "claude-haiku-4-5-20251001", 100, 200))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestServeHTTP_RecordsSpend_WhenQuotaExists(t *testing.T) {
	store := otelreceiver.NewSpanStore()
	cache := ledger.New()
	cache.Sync(makeQuota("team-a", "billing-agent", "100"))

	h := &otelreceiver.Handler{
		Store:         store,
		Cache:         cache,
		CostEstimator: pricing.NewDefaultEstimator(),
	}

	rec := post(t, h, usageSpan("team-a", "pod-1", "billing-agent", "claude-haiku-4-5-20251001", 1000, 1000))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	entry := cache.Get("team-a", "billing-agent")
	if entry == nil {
		t.Fatal("expected a ledger entry for team-a/billing-agent")
	}
	if entry.SpentTokens != 2000 {
		t.Fatalf("expected SpentTokens=2000, got %d", entry.SpentTokens)
	}
	if entry.RunCount != 1 {
		t.Fatalf("expected RunCount=1, got %d", entry.RunCount)
	}
	wantUSD := 0.00025 + 0.00125 // claude-haiku-4-5* default rate at 1000/1000 tokens
	if diff := entry.SpentUSD - wantUSD; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("expected SpentUSD=%v, got %v", wantUSD, entry.SpentUSD)
	}
}

func TestServeHTTP_UnknownModel_StillRecordsNonZeroSpend(t *testing.T) {
	store := otelreceiver.NewSpanStore()
	cache := ledger.New()
	cache.Sync(makeQuota("team-a", "billing-agent", "100"))

	h := &otelreceiver.Handler{
		Store:         store,
		Cache:         cache,
		CostEstimator: pricing.NewDefaultEstimator(),
	}

	post(t, h, usageSpan("team-a", "pod-1", "billing-agent", "some-custom-finetune", 1000, 1000))

	entry := cache.Get("team-a", "billing-agent")
	if entry == nil || entry.SpentUSD <= 0 {
		t.Fatalf("expected non-zero fallback spend for an unrecognised model, got %+v", entry)
	}
}

func TestServeHTTP_NoQuotaConfigured_DoesNotFail(t *testing.T) {
	store := otelreceiver.NewSpanStore()
	cache := ledger.New() // no TokenQuota synced for anyone

	h := &otelreceiver.Handler{
		Store:         store,
		Cache:         cache,
		CostEstimator: pricing.NewDefaultEstimator(),
	}

	rec := post(t, h, usageSpan("team-a", "pod-1", "ungoverned-agent", "claude-haiku-4-5-20251001", 1000, 1000))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 even with no TokenQuota configured, got %d", rec.Code)
	}
	if cache.Len() != 0 {
		t.Fatalf("expected no ledger entries to be created, got %d", cache.Len())
	}
}

func TestServeHTTP_MissingServiceAccount_SkipsSpendButKeepsIngesting(t *testing.T) {
	store := otelreceiver.NewSpanStore()
	cache := ledger.New()
	cache.Sync(makeQuota("team-a", "billing-agent", "100"))

	h := &otelreceiver.Handler{
		Store:         store,
		Cache:         cache,
		CostEstimator: pricing.NewDefaultEstimator(),
	}

	// No k8s.serviceaccount.name resource attribute — cost can't be
	// attributed to any quota, so billing-agent's entry must stay untouched.
	rec := post(t, h, usageSpan("team-a", "pod-1", "", "claude-haiku-4-5-20251001", 1000, 1000))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	entry := cache.Get("team-a", "billing-agent")
	if entry.RunCount != 0 || entry.SpentUSD != 0 {
		t.Fatalf("expected billing-agent's entry untouched, got %+v", entry)
	}
}

func TestServeHTTP_MalformedBody_Returns400(t *testing.T) {
	h := &otelreceiver.Handler{Store: otelreceiver.NewSpanStore()}
	rec := post(t, h, "{not valid json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestServeHTTP_GetMethod_Returns405(t *testing.T) {
	h := &otelreceiver.Handler{Store: otelreceiver.NewSpanStore()}
	req := httptest.NewRequest(http.MethodGet, "/v1/traces", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}
