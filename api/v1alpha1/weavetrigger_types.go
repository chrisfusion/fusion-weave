// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WeaveTriggerType defines how a WeaveTrigger activates.
// +kubebuilder:validation:Enum=OnDemand;Cron;Webhook
type WeaveTriggerType string

const (
	// TriggerOnDemand fires when the annotation fusion-platform.io/fire is set to "true".
	TriggerOnDemand WeaveTriggerType = "OnDemand"
	// TriggerCron fires on a cron schedule.
	TriggerCron WeaveTriggerType = "Cron"
	// TriggerWebhook fires on an incoming HTTP POST request.
	TriggerWebhook WeaveTriggerType = "Webhook"
)

// WeaveWebhookConfig configures the HTTP trigger endpoint.
type WeaveWebhookConfig struct {
	// Path is the URL path this webhook listens on, e.g. /trigger/my-chain.
	// +kubebuilder:validation:MinLength=2
	// +kubebuilder:validation:Pattern=`^/.*`
	Path string `json:"path"`

	// SecretRef names a Kubernetes Secret containing a "token" key used for
	// bearer token validation. If omitted the endpoint is unauthenticated.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
}

// WeaveTriggerSpec defines when and how a WeaveChain is instantiated.
type WeaveTriggerSpec struct {
	// ChainRef references the WeaveChain to instantiate on each activation.
	ChainRef corev1.LocalObjectReference `json:"chainRef"`

	// Type determines how this trigger activates.
	Type WeaveTriggerType `json:"type"`

	// Schedule is a standard cron expression (only used when Type=Cron).
	// Example: "*/5 * * * *"
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// Webhook configures the HTTP endpoint (only used when Type=Webhook).
	// +optional
	Webhook *WeaveWebhookConfig `json:"webhook,omitempty"`

	// ParameterOverrides are environment variables injected into every WeaveRun
	// created by this trigger, merged on top of per-step env vars.
	// +optional
	ParameterOverrides []corev1.EnvVar `json:"parameterOverrides,omitempty"`
}

// WeaveTriggerStatus reflects the current state of the trigger.
type WeaveTriggerStatus struct {
	// Active is true when the trigger is configured and accepting activations.
	Active bool `json:"active"`

	// LastScheduleTime is the time the cron trigger last fired.
	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`

	// LastRunName is the name of the most recently created WeaveRun.
	// +optional
	LastRunName string `json:"lastRunName,omitempty"`

	// WebhookURL is the full URL for webhook-type triggers (informational).
	// +optional
	WebhookURL string `json:"webhookURL,omitempty"`

	// PendingRuns holds names of WeaveRuns waiting for a concurrency slot.
	// +optional
	PendingRuns []string `json:"pendingRuns,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ft
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Chain",type=string,JSONPath=".spec.chainRef.name"
// +kubebuilder:printcolumn:name="Active",type=boolean,JSONPath=".status.active"
// +kubebuilder:printcolumn:name="LastRun",type=string,JSONPath=".status.lastRunName"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// WeaveTrigger defines when and how a WeaveChain is instantiated as a WeaveRun.
type WeaveTrigger struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WeaveTriggerSpec   `json:"spec,omitempty"`
	Status WeaveTriggerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WeaveTriggerList contains a list of WeaveTrigger.
type WeaveTriggerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WeaveTrigger `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WeaveTrigger{}, &WeaveTriggerList{})
}
