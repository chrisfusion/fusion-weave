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
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Skipped;Retrying;Deployed
type WeaveStepPhase string

const (
	// StepPhasePending means the step is waiting for its dependencies.
	StepPhasePending WeaveStepPhase = "Pending"
	// StepPhaseRunning means the batch/v1 Job has been submitted or the Deployment is being created.
	StepPhaseRunning WeaveStepPhase = "Running"
	// StepPhaseSucceeded means the batch/v1 Job completed successfully.
	StepPhaseSucceeded WeaveStepPhase = "Succeeded"
	// StepPhaseFailed means the batch/v1 Job failed and retries are exhausted, or the Deployment was removed.
	StepPhaseFailed WeaveStepPhase = "Failed"
	// StepPhaseSkipped means the step was not started because its condition was
	// not met or the chain was stopped.
	StepPhaseSkipped WeaveStepPhase = "Skipped"
	// StepPhaseRetrying means the step is waiting for its backoff period.
	StepPhaseRetrying WeaveStepPhase = "Retrying"
	// StepPhaseDeployed means the Deployment became Available and is actively running.
	// This phase is non-terminal: it satisfies dependency checks for downstream steps
	// but keeps the WeaveRun in Running phase for as long as the service is up.
	StepPhaseDeployed WeaveStepPhase = "Deployed"
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

// WeaveRunStepOverride provides per-step deployment parameters for a single
// deploy-kind step. When present the operator fetches runner configuration
// from the artifact's metadata.yaml in fusion-index rather than from the
// WeaveServiceTemplate, and names the Deployment <runName>-<stepName> so
// multiple runs can share the same chain without colliding.
type WeaveRunStepOverride struct {
	// StepName is the name of the deploy-kind step in the chain to override.
	// +kubebuilder:validation:MinLength=1
	StepName string `json:"stepName"`

	// ArtifactName is the full artifact name in fusion-index (e.g. "app.my-service").
	// +kubebuilder:validation:MinLength=1
	ArtifactName string `json:"artifactName"`

	// Tag is the mutable tag to track (e.g. "stable").
	// +kubebuilder:validation:MinLength=1
	Tag string `json:"tag"`

	// IngressHost is the fully-qualified domain name for this service instance
	// (e.g. "my-service.example.com"). Required when the chain step uses an Ingress.
	// +optional
	IngressHost string `json:"ingressHost,omitempty"`

	// IndexURL is the fusion-index base URL used to resolve the artifact.
	// When empty the operator falls back to the FUSION_INDEX_URL env var,
	// then to the in-cluster default http://fusion-index-backend.fusion.svc.cluster.local:8080.
	// +optional
	IndexURL string `json:"indexURL,omitempty"`
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

	// StepOverrides provides per-step deployment parameters for deploy-kind steps.
	// When a step is listed here the Deployment is named <runName>-<stepName> and
	// owned by the WeaveRun; runner configuration is read from the artifact's
	// metadata.yaml in fusion-index instead of from the WeaveServiceTemplate.
	// Non-override runs are unaffected.
	// +optional
	StepOverrides []WeaveRunStepOverride `json:"stepOverrides,omitempty"`

	// AuthSecretRefOverride overrides WeaveChainSpec.AuthSecretRef (and any
	// WeaveTriggerSpec.AuthSecretRefOverride) for this run only. Injected via
	// envFrom into every step pod of this run.
	// +optional
	AuthSecretRefOverride *corev1.LocalObjectReference `json:"authSecretRefOverride,omitempty"`
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

	// ActiveDeployments tracks run-owned Deployments created via StepOverrides.
	// The map key is the Deployment name (<runName>-<stepName>). Health monitoring
	// and code-source polling for these entries are handled by the run controller.
	// +optional
	ActiveDeployments map[string]WeaveActiveDeploymentStatus `json:"activeDeployments,omitempty"`
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
