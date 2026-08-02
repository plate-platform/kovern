// Package otelreceiver implements a minimal OTLP/HTTP trace receiver.
//
// It accepts span batches at POST /v1/traces (OTLP JSON encoding, no proto
// dependency) and records tool-call events in an in-memory SpanStore that the
// heuristic engine queries.
//
// Supported OTLP resource attributes:
//   k8s.namespace.name  — pod namespace
//   k8s.pod.name        — pod name
//
// Supported span attributes for tool-call extraction:
//   gen_ai.tool.name    — OTel GenAI semantic convention (preferred)
//   tool.name           — common agent SDK attribute
//   (span name)         — fallback
package otelreceiver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ── OTLP JSON types ──────────────────────────────────────────────────────────

type anyValue struct {
	StringValue *string `json:"stringValue,omitempty"`
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
		if namespace == "" || pod == "" {
			continue
		}
		for _, ss := range rs.ScopeSpans {
			for _, span := range ss.Spans {
				tool := extractToolName(span)
				if tool == "" {
					continue
				}
				h.Store.Record(namespace, pod, tool, parseNano(span.StartTimeUnixNano))
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
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
