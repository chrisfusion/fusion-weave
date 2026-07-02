// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WeaveTriggerType defines how a WeaveTrigger activates.
// +kubebuilder:validation:Enum=OnDemand;Cron;Webhook;BatchCron;Kafka
type WeaveTriggerType string

const (
	// TriggerOnDemand fires when the annotation fusion-platform.io/fire is set to "true".
	TriggerOnDemand WeaveTriggerType = "OnDemand"
	// TriggerCron fires on a cron schedule.
	TriggerCron WeaveTriggerType = "Cron"
	// TriggerWebhook fires on an incoming HTTP POST request.
	TriggerWebhook WeaveTriggerType = "Webhook"
	// TriggerBatchCron fires individual jobs from a YAML job list stored in a ConfigMap.
	// Each job carries its own cron schedule and metadata injected as env vars.
	TriggerBatchCron WeaveTriggerType = "BatchCron"
	// TriggerKafka fires on messages consumed from a Kafka topic.
	TriggerKafka WeaveTriggerType = "Kafka"
)

// WeaveKafkaConfig configures a generic Kafka consumer trigger.
type WeaveKafkaConfig struct {
	// Brokers is the list of Kafka bootstrap broker addresses.
	Brokers []string `json:"brokers"`

	// Topic is the Kafka topic to consume from.
	Topic string `json:"topic"`

	// ConsumerGroup is the Kafka consumer group ID.
	ConsumerGroup string `json:"consumerGroup"`

	// SecretRef optionally names a Secret containing SASL credentials.
	// Keys: "username", "password", and optionally "mechanism" (PLAIN, SCRAM-SHA-256, SCRAM-SHA-512).
	// Defaults to PLAIN when mechanism is absent.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// EventFilter restricts which S3 event types trigger a run.
	// Accepted values: "put", "delete", "get". Empty = all events.
	// +optional
	EventFilter []string `json:"eventFilter,omitempty"`

	// BucketFilter restricts which S3 bucket names trigger a run.
	// Empty = all buckets.
	// +optional
	BucketFilter []string `json:"bucketFilter,omitempty"`

	// MaxConcurrentRuns caps the number of active WeaveRuns for this trigger.
	// Events received while the cap is reached are skipped (offset committed).
	// 0 = unlimited.
	// +optional
	MaxConcurrentRuns int `json:"maxConcurrentRuns,omitempty"`
}

// WeaveBatchCronConfig references the ConfigMap that holds the batch job list YAML.
type WeaveBatchCronConfig struct {
	// JobsConfigMapRef names the ConfigMap whose "jobs.yaml" key contains the batch job list.
	JobsConfigMapRef corev1.LocalObjectReference `json:"jobsConfigMapRef"`
}

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

	// BatchCron configures the batch job list source (only used when Type=BatchCron).
	// +optional
	BatchCron *WeaveBatchCronConfig `json:"batchCron,omitempty"`

	// Kafka configures the Kafka consumer (only used when Type=Kafka).
	// +optional
	Kafka *WeaveKafkaConfig `json:"kafka,omitempty"`

	// Paused suspends all scheduling for this trigger when true. Runs already in
	// progress are unaffected; no new runs are created until Paused is set to false.
	// +optional
	Paused bool `json:"paused,omitempty"`

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

	// BatchJobCount is the number of valid jobs loaded from the ConfigMap (BatchCron only).
	// +optional
	BatchJobCount int `json:"batchJobCount,omitempty"`

	// BatchJobErrors is the number of job entries that failed validation (BatchCron only).
	// +optional
	BatchJobErrors int `json:"batchJobErrors,omitempty"`
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
