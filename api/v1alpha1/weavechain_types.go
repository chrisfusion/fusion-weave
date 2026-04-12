// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WeaveFailurePolicy controls what happens to the chain when a step fails terminally.
// +kubebuilder:validation:Enum=StopAll;ContinueOthers;RetryFailed
type WeaveFailurePolicy string

const (
	// FailurePolicyStopAll halts all pending and running steps immediately.
	FailurePolicyStopAll WeaveFailurePolicy = "StopAll"
	// FailurePolicyContinueOthers allows independent branches to continue.
	FailurePolicyContinueOthers WeaveFailurePolicy = "ContinueOthers"
	// FailurePolicyRetryFailed retries the failed step according to its RetryPolicy.
	FailurePolicyRetryFailed WeaveFailurePolicy = "RetryFailed"
)

// WeaveConcurrencyPolicy controls what happens when a new run is triggered
// while a previous run is still active.
// +kubebuilder:validation:Enum=Wait;Forbid
type WeaveConcurrencyPolicy string

const (
	// ConcurrencyWait queues the new run until the active run completes.
	ConcurrencyWait WeaveConcurrencyPolicy = "Wait"
	// ConcurrencyForbid drops the new run if an active run exists.
	ConcurrencyForbid WeaveConcurrencyPolicy = "Forbid"
)

// WeaveStepKind selects whether a chain step runs a batch Job or a long-running Deployment.
// +kubebuilder:validation:Enum=Job;Deploy
type WeaveStepKind string

const (
	// StepKindJob runs a batch/v1 Job (default).
	StepKindJob WeaveStepKind = "Job"
	// StepKindDeploy creates or rolling-updates a long-running Deployment.
	StepKindDeploy WeaveStepKind = "Deploy"
)

// WeaveDeployHealthPhase describes the ongoing health of a managed Deployment.
// +kubebuilder:validation:Enum=Healthy;Unhealthy;RollingBack;RolledBack;Unknown
type WeaveDeployHealthPhase string

const (
	DeployHealthHealthy    WeaveDeployHealthPhase = "Healthy"
	DeployHealthUnhealthy  WeaveDeployHealthPhase = "Unhealthy"
	DeployHealthRollingBack WeaveDeployHealthPhase = "RollingBack"
	DeployHealthRolledBack WeaveDeployHealthPhase = "RolledBack"
	DeployHealthUnknown    WeaveDeployHealthPhase = "Unknown"
)

// WeaveActiveDeploymentStatus tracks the health of a Deployment created by a deploy step.
type WeaveActiveDeploymentStatus struct {
	// DeploymentName is the name of the managed Deployment.
	DeploymentName string `json:"deploymentName"`

	// StepName is the chain step that owns this Deployment.
	StepName string `json:"stepName"`

	// Health is the current health phase of the Deployment.
	Health WeaveDeployHealthPhase `json:"health"`

	// CurrentRevision is the Deployment revision currently running.
	// +optional
	CurrentRevision string `json:"currentRevision,omitempty"`

	// LastRollbackRevision is the revision rolled back to (set after a rollback).
	// +optional
	LastRollbackRevision string `json:"lastRollbackRevision,omitempty"`

	// UnhealthySince is when the Deployment first entered an unhealthy state.
	// +optional
	UnhealthySince *metav1.Time `json:"unhealthySince,omitempty"`

	// LastRollbackTime is when the most recent rollback was triggered.
	// +optional
	LastRollbackTime *metav1.Time `json:"lastRollbackTime,omitempty"`

	// UnhealthyDurationSeconds is the unhealthyDuration from the template at
	// registration time, cached here to avoid a lookup in the health loop.
	// +optional
	UnhealthyDurationSeconds int64 `json:"unhealthyDurationSeconds,omitempty"`

	// Message is a human-readable status detail.
	// +optional
	Message string `json:"message,omitempty"`
}

// WeaveChainStep defines one node in the DAG.
type WeaveChainStep struct {
	// Name uniquely identifies this step within the chain.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// StepKind selects the step type: Job (default) or Deploy.
	// Job steps run a batch/v1 Job; Deploy steps create/update a long-running Deployment.
	// +kubebuilder:default=Job
	// +optional
	StepKind WeaveStepKind `json:"stepKind,omitempty"`

	// JobTemplateRef references the WeaveJobTemplate for Job-kind steps.
	// Required when StepKind is Job (or omitted); ignored for Deploy steps.
	// +optional
	JobTemplateRef *corev1.LocalObjectReference `json:"jobTemplateRef,omitempty"`

	// ServiceTemplateRef references the WeaveServiceTemplate for Deploy-kind steps.
	// Required when StepKind is Deploy; ignored for Job steps.
	// +optional
	ServiceTemplateRef *corev1.LocalObjectReference `json:"serviceTemplateRef,omitempty"`

	// DependsOn lists the names of steps that must reach a terminal state
	// before this step is evaluated. Empty means this is a root step.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// RunOnSuccess allows this step to start when all dependencies succeeded.
	// Defaults to true.
	// +kubebuilder:default=true
	RunOnSuccess bool `json:"runOnSuccess,omitempty"`

	// RunOnFailure allows this step to start when at least one dependency failed.
	// Enables failure-branch patterns. Defaults to false.
	// +kubebuilder:default=false
	RunOnFailure bool `json:"runOnFailure,omitempty"`

	// EnvOverrides are extra environment variables injected into this step,
	// on top of those defined in the referenced WeaveJobTemplate.
	// +optional
	EnvOverrides []corev1.EnvVar `json:"envOverrides,omitempty"`

	// ProducesOutput indicates that this step writes a JSON object to stdout.
	// The operator captures the output and stores it in the run's output ConfigMap
	// so that downstream steps can consume it via ConsumesOutputFrom.
	// +optional
	ProducesOutput bool `json:"producesOutput,omitempty"`

	// ConsumesOutputFrom lists the names of steps in this chain whose captured
	// JSON output should be merged and made available to this step at
	// /weave-input/input.json inside the container.
	// Every referenced step must have ProducesOutput: true and must be a direct
	// or transitive dependency of this step.
	// +optional
	ConsumesOutputFrom []string `json:"consumesOutputFrom,omitempty"`
}

// WeaveSharedStorageSpec configures a per-run ReadWriteMany PVC that is mounted
// into every job pod at /weave-shared. The PVC is owned by the WeaveRun and is
// garbage-collected automatically when the run is deleted.
// Parallel write conflicts are the job author's responsibility.
type WeaveSharedStorageSpec struct {
	// Size is the PVC storage request (e.g. "500Mi", "2Gi").
	// +kubebuilder:validation:MinLength=1
	Size string `json:"size"`

	// StorageClassName is the name of the StorageClass to use.
	// If empty, the cluster default StorageClass is used.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// WeaveChainSpec defines the DAG topology and execution policy.
type WeaveChainSpec struct {
	// Steps is the list of DAG nodes. At least one step is required.
	// +kubebuilder:validation:MinItems=1
	Steps []WeaveChainStep `json:"steps"`

	// FailurePolicy controls chain-wide failure behaviour.
	// +kubebuilder:default=StopAll
	FailurePolicy WeaveFailurePolicy `json:"failurePolicy,omitempty"`

	// ConcurrencyPolicy controls behaviour when a new run is triggered
	// while a previous run is still active.
	// +kubebuilder:default=Wait
	ConcurrencyPolicy WeaveConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`

	// SharedStorage, when set, provisions a ReadWriteMany PVC for each run of
	// this chain and mounts it into every job pod at /weave-shared.
	// +optional
	SharedStorage *WeaveSharedStorageSpec `json:"sharedStorage,omitempty"`
}

// WeaveChainStatus reflects validation results for the chain.
type WeaveChainStatus struct {
	// ObservedGeneration is the generation last processed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Valid is true when all referenced templates exist and the DAG is acyclic.
	Valid bool `json:"valid"`

	// ValidationMessage holds a human-readable reason when Valid is false.
	// +optional
	ValidationMessage string `json:"validationMessage,omitempty"`

	// ActiveDeployments maps stable Deployment names (<chainName>-<stepName>) to
	// their ongoing health status. The operator monitors these after a run completes
	// and triggers automatic rollback when a Deployment remains unhealthy.
	// +optional
	ActiveDeployments map[string]WeaveActiveDeploymentStatus `json:"activeDeployments,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=fc
// +kubebuilder:printcolumn:name="FailurePolicy",type=string,JSONPath=".spec.failurePolicy"
// +kubebuilder:printcolumn:name="Concurrency",type=string,JSONPath=".spec.concurrencyPolicy"
// +kubebuilder:printcolumn:name="Valid",type=boolean,JSONPath=".status.valid"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// WeaveChain defines a DAG of WeaveJobTemplate references with dependency rules.
type WeaveChain struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WeaveChainSpec   `json:"spec,omitempty"`
	Status WeaveChainStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WeaveChainList contains a list of WeaveChain.
type WeaveChainList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WeaveChain `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WeaveChain{}, &WeaveChainList{})
}
