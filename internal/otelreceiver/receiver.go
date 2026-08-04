// Package otelreceiver implements a minimal OTLP/HTTP trace receiver.
//
// It accepts span batches at POST /v1/traces (OTLP JSON encoding, no proto
// dependency), records tool-call events in an in-memory SpanStore that the
// heuristic engine queries, and — when a span carries GenAI token-usage
// attributes — estimates the call's USD cost and records it against the
// reporting ServiceAccount's TokenQuota ledger entry (internal/ledger.Cache).
//
// Supported OTLP resource attributes:
//
//	k8s.namespace.name      — pod namespace
//	k8s.pod.name            — pod name
//	k8s.serviceaccount.name — ServiceAccount running the pod (required for
//	                          spend recording; the TokenQuota ledger key is
//	                          namespace+ServiceAccount, not namespace+pod)
//
// Supported span attributes for tool-call extraction:
//
//	gen_ai.tool.name    — OTel GenAI semantic convention (preferred)
//	tool.name           — common agent SDK attribute
//	(span name)         — fallback
//
// Supported span attributes for cost/spend tracking (OTel GenAI semantic
// conventions — https://opentelemetry.io/docs/specs/semconv/gen-ai/):
//
//	gen_ai.usage.input_tokens   — prompt/input token count
//	gen_ai.usage.output_tokens  — completion/output token count
//	gen_ai.request.model        — model name, matched against internal/pricing
//	                              glob patterns to estimate USD cost
//
// A span missing k8s.serviceaccount.name or both usage attributes is not an
// error — it simply isn't a billable GenAI call, or the instrumentation
// doesn't report cost data. Loop detection (tool-call recording) never
// depends on cost data being present.
package otelreceiver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/plate-platform/kovern/internal/ledger"
	"github.com/plate-platform/kovern/internal/pricing"
)

// ── OTLP JSON types ──────────────────────────────────────────────────────────

type anyValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	// IntValue and DoubleValue cover numeric span attributes such as
	// gen_ai.usage.input_tokens. Per protobuf JSON mapping, int64 values are
	// encoded as JSON strings (to avoid precision loss), so IntValue is a
	// *string like StringValue, not a numeric type.
	IntValue    *string  `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
}

type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

type otlpSpan struct {
	Name              string     `json:"name"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	Attributes        []keyValue `json:"attributes"`
}

type scopeSpans struct {
	Spans []otlpSpan `json:"spans"`
}

type resourceSpans struct {
	Resource struct {
		Attributes []keyValue `json:"attributes"`
	} `json:"resource"`
	ScopeSpans []scopeSpans `json:"scopeSpans"`
}

type traceRequest struct {
	ResourceSpans []resourceSpans `json:"resourceSpans"`
}

// ── SpanStore ────────────────────────────────────────────────────────────────

// ToolCall is a single recorded tool invocation extracted from a span.
type ToolCall struct {
	ToolName  string
	Timestamp time.Time
}

// SpanStore is a thread-safe, in-memory sliding-window store of tool calls
// keyed by "namespace/pod". The heuristic engine reads from it; the OTLP
// HTTP handler writes to it.
type SpanStore struct {
	mu      sync.RWMutex
	records map[string][]ToolCall
}

// NewSpanStore returns an empty SpanStore.
func NewSpanStore() *SpanStore {
	return &SpanStore{records: make(map[string][]ToolCall)}
}

// Record appends a tool call for the given pod.
func (s *SpanStore) Record(namespace, pod, tool string, ts time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := namespace + "/" + pod
	s.records[k] = append(s.records[k], ToolCall{ToolName: tool, Timestamp: ts})
}

// Window returns all tool calls for the given pod within the last windowSeconds.
func (s *SpanStore) Window(namespace, pod string, windowSeconds int) []ToolCall {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().Add(-time.Duration(windowSeconds) * time.Second)
	all := s.records[namespace+"/"+pod]
	out := make([]ToolCall, 0, len(all))
	for _, c := range all {
		if c.Timestamp.After(cutoff) {
			out = append(out, c)
		}
	}
	return out
}

// Trim discards tool calls older than maxAge to prevent unbounded growth.
// Call periodically (e.g. every tick of the heuristic engine).
func (s *SpanStore) Trim(maxAge time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	for k, calls := range s.records {
		keep := calls[:0]
		for _, c := range calls {
			if c.Timestamp.After(cutoff) {
				keep = append(keep, c)
			}
		}
		if len(keep) == 0 {
			delete(s.records, k)
		} else {
			s.records[k] = keep
		}
	}
}

// ── HTTP handler ─────────────────────────────────────────────────────────────

// Handler is an http.Handler that accepts OTLP/HTTP JSON trace exports at
// POST /v1/traces and writes tool-call events into Store.
type Handler struct {
	Store *SpanStore

	// Cache and CostEstimator are optional. When both are set, spans that
	// carry GenAI usage attributes and a k8s.serviceaccount.name resource
	// attribute have their estimated cost recorded via Cache.RecordSpend.
	// When either is nil, spend recording is skipped entirely — tool-call
	// recording (loop detection) is unaffected either way.
	Cache         *ledger.Cache
	CostEstimator *pricing.Estimator

	// Logger is optional; the zero value discards all output.
	Logger logr.Logger
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req traceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	for _, rs := range req.ResourceSpans {
		namespace := attrStr(rs.Resource.Attributes, "k8s.namespace.name")
		pod := attrStr(rs.Resource.Attributes, "k8s.pod.name")
		serviceAccount := attrStr(rs.Resource.Attributes, "k8s.serviceaccount.name")
		if namespace == "" || pod == "" {
			continue
		}
		for _, ss := range rs.ScopeSpans {
			for _, span := range ss.Spans {
				if tool := extractToolName(span); tool != "" {
					h.Store.Record(namespace, pod, tool, parseNano(span.StartTimeUnixNano))
				}
				h.recordSpend(namespace, serviceAccount, span)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
}

// recordSpend extracts GenAI token-usage attributes from a span and, if a
// TokenQuota ledger entry exists for the reporting ServiceAccount, records
// the estimated cost. It is a no-op whenever cost tracking isn't wired up
// (Cache/CostEstimator nil), the span isn't a billable GenAI call (no usage
// attributes), or the resource didn't report a ServiceAccount — none of
// which should ever cause span ingestion itself to fail.
func (h *Handler) recordSpend(namespace, serviceAccount string, span otlpSpan) {
	if h.Cache == nil || h.CostEstimator == nil || serviceAccount == "" {
		return
	}

	inputTokens, hasInput := attrInt64(span.Attributes, "gen_ai.usage.input_tokens")
	outputTokens, hasOutput := attrInt64(span.Attributes, "gen_ai.usage.output_tokens")
	if !hasInput && !hasOutput {
		return
	}

	model := attrStr(span.Attributes, "gen_ai.request.model")
	if model == "" {
		model = "unknown"
	}

	usd, matched := h.CostEstimator.Estimate(model, inputTokens, outputTokens)
	if !matched {
		h.Logger.V(1).Info("no price entry for model, used fallback rate",
			"model", model, "namespace", namespace, "serviceAccount", serviceAccount)
	}

	if _, err := h.Cache.RecordSpend(namespace, serviceAccount, usd, inputTokens+outputTokens); err != nil {
		// No TokenQuota configured for this ServiceAccount — expected for
		// ungoverned workloads; fail-open per ADR-003, nothing to record.
		h.Logger.V(1).Info("spend not recorded, no TokenQuota for ServiceAccount",
			"namespace", namespace, "serviceAccount", serviceAccount, "reason", err.Error())
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func attrStr(attrs []keyValue, key string) string {
	for _, a := range attrs {
		if a.Key == key && a.Value.StringValue != nil {
			return *a.Value.StringValue
		}
	}
	return ""
}

// attrInt64 reads a numeric span attribute. OTLP JSON encodes int64 as a
// string (IntValue), but DoubleValue and a numeric-looking StringValue are
// also accepted for robustness against instrumentation that encodes
// gen_ai.usage.* differently.
func attrInt64(attrs []keyValue, key string) (int64, bool) {
	for _, a := range attrs {
		if a.Key != key {
			continue
		}
		if a.Value.IntValue != nil {
			if n, err := strconv.ParseInt(*a.Value.IntValue, 10, 64); err == nil {
				return n, true
			}
		}
		if a.Value.DoubleValue != nil {
			return int64(*a.Value.DoubleValue), true
		}
		if a.Value.StringValue != nil {
			if n, err := strconv.ParseInt(*a.Value.StringValue, 10, 64); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

func extractToolName(span otlpSpan) string {
	if v := attrStr(span.Attributes, "gen_ai.tool.name"); v != "" {
		return v
	}
	if v := attrStr(span.Attributes, "tool.name"); v != "" {
		return v
	}
	return span.Name
}

func parseNano(s string) time.Time {
	if s == "" {
		return time.Now()
	}
	ns, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Now()
	}
	return time.Unix(0, ns)
}
