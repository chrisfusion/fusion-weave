// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WeaveRunPhase is the overall lifecycle phase of a WeaveRun.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Stopped
type WeaveRunPhase string

const (
	// RunPhasePending means the run is waiting for a concurrency slot.
	RunPhasePending WeaveRunPhase = "Pending"
	// RunPhaseRunning means the DAG is actively executing.
	RunPhaseRunning WeaveRunPhase = "Running"
	// RunPhaseSucceeded means all required steps completed successfully.
	RunPhaseSucceeded WeaveRunPhase = "Succeeded"
	// RunPhaseFailed means the run ended with at least one terminal step failure.
	RunPhaseFailed WeaveRunPhase = "Failed"
	// RunPhaseStopped means the run was halted by the StopAll failure policy.
	RunPhaseStopped WeaveRunPhase = "Stopped"
)

// WeaveStepPhase is the lifecycle phase of a single DAG step.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Skipped;Retrying
type WeaveStepPhase string

const (
	// StepPhasePending means the step is waiting for its dependencies.
	StepPhasePending WeaveStepPhase = "Pending"
	// StepPhaseRunning means the batch/v1 Job has been submitted.
	StepPhaseRunning WeaveStepPhase = "Running"
	// StepPhaseSucceeded means the batch/v1 Job completed successfully.
	StepPhaseSucceeded WeaveStepPhase = "Succeeded"
	// StepPhaseFailed means the batch/v1 Job failed and retries are exhausted.
	StepPhaseFailed WeaveStepPhase = "Failed"
	// StepPhaseSkipped means the step was not started because its condition was
	// not met or the chain was stopped.
	StepPhaseSkipped WeaveStepPhase = "Skipped"
	// StepPhaseRetrying means the step is waiting for its backoff period.
	StepPhaseRetrying WeaveStepPhase = "Retrying"
)

// WeaveRunStepStatus tracks the execution state of one DAG step.
type WeaveRunStepStatus struct {
	// Name matches the WeaveChainStep name.
	Name string `json:"name"`

	// Phase is the current lifecycle phase of this step.
	Phase WeaveStepPhase `json:"phase"`

	// JobRef is the name of the batch/v1 Job created for this step.
	// +optional
	JobRef *corev1.LocalObjectReference `json:"jobRef,omitempty"`

	// RetryCount tracks how many times this step has been retried.
	// +optional
	RetryCount int32 `json:"retryCount,omitempty"`

	// NextRetryAfter is the earliest time the step will be retried.
	// +optional
	NextRetryAfter *metav1.Time `json:"nextRetryAfter,omitempty"`

	// StartTime is when the batch Job was first submitted.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the step reached a terminal phase.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message holds a human-readable status detail or failure reason.
	// +optional
	Message string `json:"message,omitempty"`

	// OutputCaptured is true once the step's JSON stdout has been successfully
	// written to the run's output ConfigMap. Only meaningful when the step's
	// WeaveChainStep has ProducesOutput: true.
	// +optional
	OutputCaptured bool `json:"outputCaptured,omitempty"`

	// DeploymentRef is the name of the apps/v1 Deployment created for Deploy-kind steps.
	// Nil for Job-kind steps.
	// +optional
	DeploymentRef *corev1.LocalObjectReference `json:"deploymentRef,omitempty"`
}

// WeaveRunSpec defines the immutable parameters of one chain execution.
type WeaveRunSpec struct {
	// ChainRef is the WeaveChain this run executes. Immutable after creation.
	ChainRef corev1.LocalObjectReference `json:"chainRef"`

	// TriggerRef identifies the WeaveTrigger that created this run, if any.
	// +optional
	TriggerRef *corev1.LocalObjectReference `json:"triggerRef,omitempty"`

	// ParameterOverrides are injected as extra environment variables into all
	// steps of this run. Useful for passing runtime context from webhook payloads.
	// +optional
	ParameterOverrides []corev1.EnvVar `json:"parameterOverrides,omitempty"`
}

// WeaveRunStatus reflects the live execution state of the run.
type WeaveRunStatus struct {
	// Phase is the overall lifecycle phase of this run.
	// +optional
	Phase WeaveRunPhase `json:"phase,omitempty"`

	// Steps holds the per-step execution state, indexed by step name.
	// +optional
	Steps []WeaveRunStepStatus `json:"steps,omitempty"`

	// StartTime is when the run moved from Pending to Running.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the run reached a terminal phase.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message holds a human-readable summary of the current state.
	// +optional
	Message string `json:"message,omitempty"`

	// SharedPVCName is the name of the per-run shared PVC, set once the PVC has
	// been successfully created. Empty when the chain has no SharedStorage config.
	// +optional
	SharedPVCName string `json:"sharedPVCName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=fr
// +kubebuilder:printcolumn:name="Chain",type=string,JSONPath=".spec.chainRef.name"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Start",type=date,JSONPath=".status.startTime"
// +kubebuilder:printcolumn:name="End",type=date,JSONPath=".status.completionTime"

// WeaveRun represents one live execution of a WeaveChain.
type WeaveRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WeaveRunSpec   `json:"spec,omitempty"`
	Status WeaveRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WeaveRunList contains a list of WeaveRun.
type WeaveRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WeaveRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WeaveRun{}, &WeaveRunList{})
}
