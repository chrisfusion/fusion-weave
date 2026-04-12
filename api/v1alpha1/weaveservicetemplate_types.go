// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WeaveServicePort defines a port exposed by the Deployment and Service.
type WeaveServicePort struct {
	// Name is a symbolic name for the port (e.g. "http").
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Port is the port number exposed on the Service.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	// TargetPort is the port number the container listens on.
	// Defaults to Port when omitted.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	TargetPort int32 `json:"targetPort,omitempty"`

	// Protocol is the network protocol. Defaults to TCP.
	// +kubebuilder:default=TCP
	// +kubebuilder:validation:Enum=TCP;UDP;SCTP
	Protocol corev1.Protocol `json:"protocol,omitempty"`
}

// WeaveIngressRule defines one host/path routing rule.
type WeaveIngressRule struct {
	// Host is the fully-qualified domain name for the rule.
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`

	// Path is the URL path prefix (e.g. "/"). Defaults to "/".
	// +kubebuilder:default="/"
	Path string `json:"path,omitempty"`

	// PathType controls how the path is matched.
	// +kubebuilder:default=Prefix
	// +kubebuilder:validation:Enum=Exact;Prefix;ImplementationSpecific
	PathType string `json:"pathType,omitempty"`

	// ServicePort is the name or number of the Service port to route to.
	// Must match one of the ports declared in the WeaveServiceTemplate.
	// +kubebuilder:validation:MinLength=1
	ServicePort string `json:"servicePort"`
}

// WeaveIngressSpec defines the Ingress the operator manages for this service.
type WeaveIngressSpec struct {
	// IngressClassName selects the Ingress controller (e.g. "nginx").
	// +optional
	IngressClassName *string `json:"ingressClassName,omitempty"`

	// Rules is the list of host/path routing rules. At least one is required.
	// +kubebuilder:validation:MinItems=1
	Rules []WeaveIngressRule `json:"rules"`

	// TLSSecretName, when set, enables TLS termination using the named Secret.
	// The Secret must exist in the same namespace.
	// +optional
	TLSSecretName string `json:"tlsSecretName,omitempty"`
}

// WeaveServiceTemplateSpec defines the desired state of a long-running Deployment.
type WeaveServiceTemplateSpec struct {
	// Image is the container image to run.
	// +kubebuilder:validation:MinLength=1
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

	// ServiceAccountName is the pod service account to use.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Replicas is the desired number of pod replicas.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Replicas int32 `json:"replicas,omitempty"`

	// Ports declares the ports the container exposes and that will be registered
	// on the Service. At least one port is required.
	// +kubebuilder:validation:MinItems=1
	Ports []WeaveServicePort `json:"ports"`

	// LivenessProbe is the liveness probe for the container.
	// +optional
	LivenessProbe *corev1.Probe `json:"livenessProbe,omitempty"`

	// ReadinessProbe is the readiness probe for the container.
	// +optional
	ReadinessProbe *corev1.Probe `json:"readinessProbe,omitempty"`

	// StartupProbe is the startup probe for the container.
	// +optional
	StartupProbe *corev1.Probe `json:"startupProbe,omitempty"`

	// ServiceType controls the type of Service created.
	// +kubebuilder:default=ClusterIP
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	ServiceType corev1.ServiceType `json:"serviceType,omitempty"`

	// Ingress, when set, causes the operator to create and manage an Ingress
	// resource routing external traffic to the Service.
	// +optional
	Ingress *WeaveIngressSpec `json:"ingress,omitempty"`

	// UnhealthyDuration is the amount of time a Deployment may remain in an
	// unhealthy state before the operator triggers an automatic rollback.
	// Value is a Go duration string (e.g. "5m", "10m30s"). Defaults to "5m".
	// +kubebuilder:default="5m"
	UnhealthyDuration string `json:"unhealthyDuration,omitempty"`

	// RevisionHistoryLimit is the number of old ReplicaSets to retain for
	// rollback. Defaults to 5 (minimum 1).
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	RevisionHistoryLimit *int32 `json:"revisionHistoryLimit,omitempty"`
}

// WeaveServiceTemplateStatus reflects validation results for the template.
type WeaveServiceTemplateStatus struct {
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
// +kubebuilder:resource:shortName=wst
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=".spec.image"
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=".spec.replicas"
// +kubebuilder:printcolumn:name="Valid",type=boolean,JSONPath=".status.valid"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// WeaveServiceTemplate defines a reusable long-running Deployment specification
// referenced by deploy-kind steps in a WeaveChain.
type WeaveServiceTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WeaveServiceTemplateSpec   `json:"spec,omitempty"`
	Status WeaveServiceTemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WeaveServiceTemplateList contains a list of WeaveServiceTemplate.
type WeaveServiceTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WeaveServiceTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WeaveServiceTemplate{}, &WeaveServiceTemplateList{})
}
