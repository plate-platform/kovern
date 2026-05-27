package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LivelockPolicySpec defines how loop/livelock detection is configured.
type LivelockPolicySpec struct {
	// Selector matches agent pods this policy governs.
	Selector metav1.LabelSelector `json:"selector"`

	// Detection configures the livelock detection criteria.
	Detection DetectionCriteria `json:"detection"`

	// Remediation defines what action to take when a livelock is detected.
	Remediation RemediationSpec `json:"remediation"`
}

// DetectionCriteria configures the detection engines.
type DetectionCriteria struct {
	// OTel enables span-based heuristic detection (primary, always available).
	// +optional
	OTel *OTelCriteria `json:"otel,omitempty"`

	// Semantic enables Claude-powered semantic similarity detection (optional, requires API key).
	// +optional
	Semantic *SemanticCriteria `json:"semantic,omitempty"`
}

// OTelCriteria configures heuristic loop detection from OpenTelemetry spans.
type OTelCriteria struct {
	// MaxSameToolCalls is the number of times the same tool can be called within
	// the sliding window before a loop is flagged. Defaults to 3.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	MaxSameToolCalls int `json:"maxSameToolCalls,omitempty"`

	// WindowSeconds is the duration of the sliding observation window. Defaults to 60.
	// +kubebuilder:default=60
	// +kubebuilder:validation:Minimum=5
	WindowSeconds int `json:"windowSeconds,omitempty"`

	// IdenticalResponseHash flags runs where consecutive LLM responses have the same hash.
	// +kubebuilder:default=true
	// +optional
	IdenticalResponseHash bool `json:"identicalResponseHash,omitempty"`
}

// SemanticCriteria configures Claude-based semantic livelock detection.
// Requires ANTHROPIC_API_KEY to be set in the operator environment.
type SemanticCriteria struct {
	// Enabled activates semantic detection. Disabled by default to avoid API costs.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// WindowTurns is the number of recent agent turns to analyse. Defaults to 3.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:validation:Maximum=10
	// +optional
	WindowTurns int `json:"windowTurns,omitempty"`

	// SimilarityThreshold triggers eviction when semantic similarity exceeds this value.
	// Uses Claude to evaluate whether recent turns are semantically equivalent loops.
	// Defaults to 0.92.
	// +kubebuilder:default="0.92"
	// +optional
	SimilarityThreshold string `json:"similarityThreshold,omitempty"`

	// Model is the Claude model used for semantic analysis.
	// Defaults to claude-haiku-4-5 for low latency and cost.
	// +kubebuilder:default="claude-haiku-4-5-20251001"
	// +optional
	Model string `json:"model,omitempty"`
}

// RemediationSpec defines how Kovern responds to a detected livelock.
type RemediationSpec struct {
	// Action is the primary remediation response.
	// +kubebuilder:validation:Enum=InjectFault;EvictPod;SuspendWorkload
	// +kubebuilder:default=InjectFault
	Action RemediationAction `json:"action,omitempty"`

	// FaultConfig is used when Action is InjectFault.
	// +optional
	FaultConfig *FaultConfig `json:"faultConfig,omitempty"`

	// FallbackAction is executed if the primary Action fails or the loop persists.
	// +kubebuilder:validation:Enum=EvictPod;SuspendWorkload
	// +kubebuilder:default=EvictPod
	// +optional
	FallbackAction RemediationAction `json:"fallbackAction,omitempty"`

	// EmitEvent controls whether a Kubernetes Warning Event is emitted on detection.
	// +kubebuilder:default=true
	// +optional
	EmitEvent bool `json:"emitEvent,omitempty"`

	// GitOpsIncident triggers a commit to the agents-gitops repository with an incident report.
	// +kubebuilder:default=false
	// +optional
	GitOpsIncident bool `json:"gitopsIncident,omitempty"`
}

// RemediationAction describes a livelock response action.
type RemediationAction string

const (
	// RemediationInjectFault returns HTTP 429 to the agent to break the loop gracefully.
	RemediationInjectFault RemediationAction = "InjectFault"
	// RemediationEvictPod immediately deletes the agent Pod.
	RemediationEvictPod RemediationAction = "EvictPod"
	// RemediationSuspendWorkload scales the agent Deployment to 0 replicas.
	RemediationSuspendWorkload RemediationAction = "SuspendWorkload"
)

// FaultConfig configures the fault-injection response.
type FaultConfig struct {
	// HTTPStatusCode returned to the agent. Defaults to 429.
	// +kubebuilder:default=429
	// +optional
	HTTPStatusCode int `json:"httpStatusCode,omitempty"`

	// Duration is how long to inject the fault before attempting the fallback action.
	// +kubebuilder:default="60s"
	// +optional
	Duration string `json:"duration,omitempty"`
}

// LivelockPolicyStatus is the observed state of the policy.
type LivelockPolicyStatus struct {
	// DetectionCount is the total number of livelocks detected since creation.
	// +optional
	DetectionCount int64 `json:"detectionCount,omitempty"`

	// LastDetection is the timestamp of the most recent livelock event.
	// +optional
	LastDetection *metav1.Time `json:"lastDetection,omitempty"`

	// LastAffectedPod is the name of the last pod that was remediated.
	// +optional
	LastAffectedPod string `json:"lastAffectedPod,omitempty"`

	// Conditions holds standard K8s condition entries.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=llp,categories=kovern
// +kubebuilder:printcolumn:name="Detections",type=integer,JSONPath=`.status.detectionCount`
// +kubebuilder:printcolumn:name="Last Detection",type=date,JSONPath=`.status.lastDetection`
// +kubebuilder:printcolumn:name="Semantic",type=boolean,JSONPath=`.spec.detection.semantic.enabled`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// LivelockPolicy configures livelock/infinite-loop detection for AI agent pods.
// Kovern watches OTel spans (and optionally uses Claude) to detect stuck agents
// and applies the configured remediation action.
type LivelockPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LivelockPolicySpec   `json:"spec,omitempty"`
	Status LivelockPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LivelockPolicyList contains a list of LivelockPolicy.
type LivelockPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LivelockPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LivelockPolicy{}, &LivelockPolicyList{})
}
