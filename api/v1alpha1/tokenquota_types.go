package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TokenQuotaSpec defines the budget governance rules for an AI agent workload.
type TokenQuotaSpec struct {
	// TargetRef identifies the ServiceAccount this quota applies to.
	TargetRef TargetRef `json:"targetRef"`

	// BillingScope defines the financial budget and renewal cadence.
	BillingScope BillingScope `json:"billingScope"`

	// Limits sets optional per-model-group token caps.
	// +optional
	Limits []ModelLimit `json:"limits,omitempty"`

	// EnforcementAction determines what Kovern does when the quota is exceeded.
	// +kubebuilder:validation:Enum=SuspendWorkload;ScaleToZero;Evict
	// +kubebuilder:default=SuspendWorkload
	EnforcementAction EnforcementAction `json:"enforcementAction,omitempty"`
}

// TargetRef identifies the K8s object this quota is scoped to.
type TargetRef struct {
	// Kind of the target. Currently only ServiceAccount is supported.
	// +kubebuilder:validation:Enum=ServiceAccount
	// +kubebuilder:default=ServiceAccount
	Kind string `json:"kind,omitempty"`

	// Name of the ServiceAccount.
	Name string `json:"name"`
}

// BillingScope defines the financial ceiling and renewal behaviour.
type BillingScope struct {
	// Currency code (ISO 4217). Defaults to USD.
	// +kubebuilder:default="USD"
	// +optional
	Currency string `json:"currency,omitempty"`

	// MaxFinancialBudget is the hard cap in the given currency per renewal period.
	MaxFinancialBudget resource.Quantity `json:"maxFinancialBudget"`

	// RenewalInterval controls when the ledger resets.
	// +kubebuilder:validation:Enum=Hourly;Daily;Monthly
	// +kubebuilder:default=Monthly
	// +optional
	RenewalInterval RenewalInterval `json:"renewalInterval,omitempty"`

	// SoftLimitPct emits a Warning K8s Event at this spend percentage. Defaults to 80.
	// +kubebuilder:default=80
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=99
	// +optional
	SoftLimitPct int `json:"softLimitPct,omitempty"`
}

// ModelLimit caps token usage for a specific model group.
type ModelLimit struct {
	// ModelGroup is a glob pattern matched against the model name reported in OTel spans
	// (e.g. "claude-*", "gpt-4*").
	ModelGroup string `json:"modelGroup"`

	// MaxInputTokens caps input token consumption for this model group.
	// +optional
	MaxInputTokens int64 `json:"maxInputTokens,omitempty"`

	// MaxOutputTokens caps output token consumption for this model group.
	// +optional
	MaxOutputTokens int64 `json:"maxOutputTokens,omitempty"`
}

// RenewalInterval describes how often the quota ledger resets.
type RenewalInterval string

const (
	RenewalHourly  RenewalInterval = "Hourly"
	RenewalDaily   RenewalInterval = "Daily"
	RenewalMonthly RenewalInterval = "Monthly"
)

// EnforcementAction determines what happens when the quota is breached.
type EnforcementAction string

const (
	// EnforcementSuspendWorkload scales the Deployment to 0 replicas.
	EnforcementSuspendWorkload EnforcementAction = "SuspendWorkload"
	// EnforcementScaleToZero is an alias for SuspendWorkload.
	EnforcementScaleToZero EnforcementAction = "ScaleToZero"
	// EnforcementEvict deletes the running Pod immediately.
	EnforcementEvict EnforcementAction = "Evict"
)

// QuotaState is the current enforcement state of a TokenQuota.
type QuotaState string

const (
	// QuotaStateActive means the workload is within budget.
	QuotaStateActive QuotaState = "Active"
	// QuotaStateSoftLimit means spend has crossed the soft-limit threshold; a Warning Event has been emitted.
	QuotaStateSoftLimit QuotaState = "SoftLimit"
	// QuotaStateExceeded means the hard budget has been breached; new runs are denied.
	QuotaStateExceeded QuotaState = "Exceeded"
	// QuotaStateSuspended means enforcement has been applied and the workload is stopped.
	QuotaStateSuspended QuotaState = "Suspended"
)

// TokenQuotaStatus is the observed runtime state of the quota.
type TokenQuotaStatus struct {
	// SpentUSD is the total cost recorded in the current renewal period.
	// +optional
	SpentUSD string `json:"spentUSD,omitempty"`

	// SpentTokens is the total token count in the current renewal period.
	// +optional
	SpentTokens int64 `json:"spentTokens,omitempty"`

	// RunCount is the total number of agent runs recorded.
	// +optional
	RunCount int64 `json:"runCount,omitempty"`

	// State is the current quota enforcement state.
	// +optional
	State QuotaState `json:"state,omitempty"`

	// LastUpdated is the timestamp of the most recent ledger entry.
	// +optional
	LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`

	// NextRenewal is when the quota ledger will automatically reset.
	// +optional
	NextRenewal *metav1.Time `json:"nextRenewal,omitempty"`

	// Conditions holds standard K8s condition entries.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=tq,categories=kovern
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Spent",type=string,JSONPath=`.status.spentUSD`
// +kubebuilder:printcolumn:name="Budget",type=string,JSONPath=`.spec.billingScope.maxFinancialBudget`
// +kubebuilder:printcolumn:name="Renewal",type=string,JSONPath=`.spec.billingScope.renewalInterval`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TokenQuota enforces financial and token-usage budgets for AI agent workloads.
// Attach it to a ServiceAccount; Kovern's admission webhook blocks new runs once
// the budget is exhausted.
type TokenQuota struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TokenQuotaSpec   `json:"spec,omitempty"`
	Status TokenQuotaStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TokenQuotaList contains a list of TokenQuota.
type TokenQuotaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TokenQuota `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TokenQuota{}, &TokenQuotaList{})
}
