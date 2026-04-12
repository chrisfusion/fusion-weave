// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WeaveJobTemplateSpec defines a reusable, parameterized job specification.
type WeaveJobTemplateSpec struct {
	// Image is the container image to run.
	Image string `json:"image"`

	// Command overrides the container entrypoint.
	// +optional
	Command []string `json:"command,omitempty"`

	// Args are passed to the command.
	// +optional
	Args []string `json:"args,omitempty"`

	// Env are static environment variables injected into the container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Resources sets CPU and memory requests and limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Volumes declares secrets and configmaps to mount into the container.
	// +optional
	Volumes []WeaveVolumeMount `json:"volumes,omitempty"`

	// RetryPolicy controls how failed jobs are retried.
	// +optional
	RetryPolicy *WeaveRetryPolicy `json:"retryPolicy,omitempty"`

	// Parallelism is the maximum number of parallel pods.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Parallelism int32 `json:"parallelism,omitempty"`

	// Completions is the desired number of successful pod completions.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Completions int32 `json:"completions,omitempty"`

	// ActiveDeadlineSeconds is the job-level timeout in seconds.
	// +optional
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	// ServiceAccountName is the pod service account to use.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
}

// WeaveVolumeMount describes a secret or configmap mounted into the container.
type WeaveVolumeMount struct {
	// Name is the volume name within the pod spec.
	Name string `json:"name"`

	// MountPath is the filesystem path inside the container.
	MountPath string `json:"mountPath"`

	// SecretName mounts a Kubernetes Secret. Mutually exclusive with ConfigMapName.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// ConfigMapName mounts a Kubernetes ConfigMap. Mutually exclusive with SecretName.
	// +optional
	ConfigMapName string `json:"configMapName,omitempty"`
}

// WeaveRetryPolicy controls retry behaviour on job failure.
type WeaveRetryPolicy struct {
	// MaxRetries is the maximum number of retry attempts.
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	MaxRetries int32 `json:"maxRetries"`

	// BackoffSeconds is the delay in seconds before each retry attempt.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	BackoffSeconds int32 `json:"backoffSeconds"`
}

// WeaveJobTemplateStatus reflects validation results for the template.
type WeaveJobTemplateStatus struct {
	// ObservedGeneration is the generation last processed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Valid is true when the spec passed all validation checks.
	Valid bool `json:"valid"`

	// ValidationMessage holds a human-readable reason when Valid is false.
	// +optional
	ValidationMessage string `json:"validationMessage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=fjt
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=".spec.image"
// +kubebuilder:printcolumn:name="Valid",type=boolean,JSONPath=".status.valid"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// WeaveJobTemplate defines a reusable job specification referenced by WeaveChain steps.
type WeaveJobTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WeaveJobTemplateSpec   `json:"spec,omitempty"`
	Status WeaveJobTemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WeaveJobTemplateList contains a list of WeaveJobTemplate.
type WeaveJobTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WeaveJobTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WeaveJobTemplate{}, &WeaveJobTemplateList{})
}
