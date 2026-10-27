// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WeaveAppType selects what a WeaveApp deploys.
// +kubebuilder:validation:Enum=service;job
type WeaveAppType string

const (
	// WeaveAppTypeService runs a long-lived Deployment + Service (+ Ingress).
	WeaveAppTypeService WeaveAppType = "service"
	// WeaveAppTypeJob runs one batch Job per execution of an entrypoint.
	WeaveAppTypeJob WeaveAppType = "job"
)

// WeaveAppPhase is the coarse lifecycle phase of a WeaveApp.
// +kubebuilder:validation:Enum=Pending;Ready;Running;Invalid
type WeaveAppPhase string

const (
	// WeaveAppPhasePending: valid, but the Deployment is not available yet.
	WeaveAppPhasePending WeaveAppPhase = "Pending"
	// WeaveAppPhaseReady: service available, or job app idle and scheduled.
	WeaveAppPhaseReady WeaveAppPhase = "Ready"
	// WeaveAppPhaseRunning: at least one job execution is active.
	WeaveAppPhaseRunning WeaveAppPhase = "Running"
	// WeaveAppPhaseInvalid: the spec failed validation; see status.validationMessage.
	WeaveAppPhaseInvalid WeaveAppPhase = "Invalid"
)

// WeaveAppExecutionPhase is the phase of one job execution.
// +kubebuilder:validation:Enum=Running;Succeeded;Failed
type WeaveAppExecutionPhase string

const (
	WeaveAppExecutionRunning   WeaveAppExecutionPhase = "Running"
	WeaveAppExecutionSucceeded WeaveAppExecutionPhase = "Succeeded"
	WeaveAppExecutionFailed    WeaveAppExecutionPhase = "Failed"
)

// WeaveAppWritablePath is one writable emptyDir mount.
type WeaveAppWritablePath struct {
	// MountPath is the absolute path inside the container (e.g. /var/cache/nginx).
	// +kubebuilder:validation:Pattern=`^/.+`
	MountPath string `json:"mountPath"`

	// SizeLimit caps the emptyDir (e.g. 200Mi); the pod is evicted when it is exceeded.
	// +optional
	SizeLimit *resource.Quantity `json:"sizeLimit,omitempty"`

	// Medium "Memory" backs the emptyDir with tmpfs (counts against the memory limit).
	// +kubebuilder:validation:Enum=Memory
	// +optional
	Medium corev1.StorageMedium `json:"medium,omitempty"`
}

// WeaveAppService configures the Deployment/Service/Ingress of a type=service app.
type WeaveAppService struct {
	// Ports exposed by the container. May be omitted when codeSource is set and
	// the artifact's metadata.yaml declares runner.port.
	// +optional
	Ports []WeaveServicePort `json:"ports,omitempty"`

	// Replicas is the desired pod count. Defaults to 1.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// ServiceType of the generated Service. Defaults to ClusterIP.
	// +optional
	ServiceType corev1.ServiceType `json:"serviceType,omitempty"`

	// IngressName is the shorthand for the common case: one host
	// "<ingressName>.<ingress.hostSuffix>", path "/" (or the artifact's
	// metadata.yaml ingress.path), Prefix match. Mutually exclusive with Ingress.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	IngressName string `json:"ingressName,omitempty"`

	// Ingress gives full control (several rules, path/pathType, ingressClassName,
	// tlsSecretName). Instance-wide annotations, labels and TLS from the Helm
	// ingress.* values are applied on top, exactly as for WeaveServiceTemplate.
	// Mutually exclusive with IngressName.
	// +optional
	Ingress *WeaveAppIngress `json:"ingress,omitempty"`

	// IngressAnnotations fine-tune this app's Ingress for the cluster's
	// controller (e.g. traefik.ingress.kubernetes.io/* or
	// nginx.ingress.kubernetes.io/*). They extend, and on a key collision
	// override, the instance-wide ingress.annotations and the runner-type
	// ingress.runnerAnnotations. Additive: a key removed here stays on an
	// existing Ingress. Requires ingressName or ingress.
	// +optional
	IngressAnnotations map[string]string `json:"ingressAnnotations,omitempty"`

	// LivenessProbe, ReadinessProbe and StartupProbe configure container probes.
	// +optional
	LivenessProbe *corev1.Probe `json:"livenessProbe,omitempty"`
	// +optional
	ReadinessProbe *corev1.Probe `json:"readinessProbe,omitempty"`
	// +optional
	StartupProbe *corev1.Probe `json:"startupProbe,omitempty"`
}

// WeaveAppIngress mirrors WeaveIngressSpec with an optional servicePort.
type WeaveAppIngress struct {
	// IngressClassName selects the Ingress controller (e.g. "nginx").
	// +optional
	IngressClassName *string `json:"ingressClassName,omitempty"`

	// Rules is the list of host/path routing rules (at least one).
	// +kubebuilder:validation:MinItems=1
	Rules []WeaveAppIngressRule `json:"rules"`

	// TLSSecretName enables TLS termination with the named Secret; it wins over
	// the generated "<ingress>-tls" name of the instance-wide ingress.tls.enabled.
	// +optional
	TLSSecretName string `json:"tlsSecretName,omitempty"`
}

// WeaveAppIngressRule is one host/path routing rule.
type WeaveAppIngressRule struct {
	// Name is the leftmost DNS label; the cluster-wide ingress host suffix is
	// appended by the operator (see WeaveIngressRule.Name).
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Path is the URL path (default "/"). An artifact's metadata.yaml
	// ingress.path replaces the path of the first rule, as for run-owned steps.
	// +kubebuilder:default="/"
	// +optional
	Path string `json:"path,omitempty"`

	// PathType controls how the path is matched.
	// +kubebuilder:default=Prefix
	// +kubebuilder:validation:Enum=Exact;Prefix;ImplementationSpecific
	// +optional
	PathType string `json:"pathType,omitempty"`

	// ServicePort is the name or number of the Service port to route to.
	// Defaults to the artifact's runner.port, else the first declared port.
	// +optional
	ServicePort string `json:"servicePort,omitempty"`
}

// WeaveAppEntrypoint is one runnable command of a type=job app.
type WeaveAppEntrypoint struct {
	// Name identifies the entrypoint (DNS label, unique within the app).
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=40
	Name string `json:"name"`

	// Command overrides the app-level command for this entrypoint.
	// +optional
	Command []string `json:"command,omitempty"`

	// Args overrides the app-level args for this entrypoint.
	// +optional
	Args []string `json:"args,omitempty"`

	// Env is merged over the app-level env (this entrypoint wins).
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Schedule is a 6-field cron expression with seconds first (same format as
	// WeaveTrigger.spec.schedule). Empty = on-demand only, fired by setting the
	// annotation fusion-platform.io/fire=<entrypoint> on the WeaveApp.
	// +optional
	Schedule string `json:"schedule,omitempty"`
}

// WeaveAppSpec is a single deployable unit: one service or one job (with one or
// more entrypoints). It replaces the template + chain + trigger + run objects
// for the simple one-step case; everything it creates is owned by the WeaveApp.
type WeaveAppSpec struct {
	// Type is immutable after creation.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="type is immutable"
	Type WeaveAppType `json:"type"`

	// Image is the container image to run. With codeSource set this is the runner
	// image (e.g. the streamlit runner) that executes the loaded artifact.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// CodeSource loads the artifact built by fusion-forge from fusion-index into
	// the container via the code-loader init container.
	// +optional
	CodeSource *CodeSourceSpec `json:"codeSource,omitempty"`

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

	// WritablePaths mounts a writable emptyDir at each path into the app container
	// (and the code-loader init container), for images that run with
	// readOnlyRootFilesystem: true. Appended to the operator-wide WRITABLE_PATHS
	// (codeSource apps) and the runner-type codeSource.runnerWritablePaths; for a
	// path already mounted by those, an entry here only sets sizeLimit/medium.
	// +optional
	WritablePaths []WeaveAppWritablePath `json:"writablePaths,omitempty"`

	// ServiceAccountName is the pod service account to use.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// PodSecurityContext overrides the operator-wide workload security default.
	// +optional
	PodSecurityContext *corev1.PodSecurityContext `json:"podSecurityContext,omitempty"`

	// ContainerSecurityContext overrides the operator-wide workload security default.
	// +optional
	ContainerSecurityContext *corev1.SecurityContext `json:"containerSecurityContext,omitempty"`

	// Service configures a type=service app. Must be unset for type=job.
	// +optional
	Service *WeaveAppService `json:"service,omitempty"`

	// Entrypoints lists the runnable commands of a type=job app (at least one).
	// Must be empty for type=service.
	// +optional
	Entrypoints []WeaveAppEntrypoint `json:"entrypoints,omitempty"`

	// ExternalAuthRef injects an OIDC/ServiceAccount token into every job
	// execution (same mechanism and allowlists as WeaveChain.spec.externalAuthRef).
	// Only supported for type=job.
	// +optional
	ExternalAuthRef *WeaveExternalAuthRef `json:"externalAuthRef,omitempty"`

	// UnsafeEnvironmentInjector, when true or unset, also exposes the external
	// auth token as WEAVE_EXTERNAL_AUTH_TOKEN (the file mount is always present).
	// Same default as WeaveChain.spec.unsafeEnvironmentInjector.
	// +optional
	UnsafeEnvironmentInjector *bool `json:"unsafeEnvironmentInjector,omitempty"`

	// ActiveDeadlineSeconds is the per-execution job timeout (type=job).
	// +optional
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	// ExecutionTTLSeconds is how long finished Jobs (and their pod logs) are kept
	// (type=job). Defaults to 86400 (24h). The result stays in status.executions.
	// +kubebuilder:validation:Minimum=0
	// +optional
	ExecutionTTLSeconds *int32 `json:"executionTTLSeconds,omitempty"`
}

// WeaveAppExecution records one job execution; kept in status after the Job is
// garbage-collected by its TTL.
type WeaveAppExecution struct {
	Entrypoint string                 `json:"entrypoint"`
	JobName    string                 `json:"jobName"`
	Phase      WeaveAppExecutionPhase `json:"phase"`
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

// WeaveAppStatus is the observed state. All fields are optional so a
// MergeFrom-diffed patch can never trip a "Required value" error.
type WeaveAppStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	Phase WeaveAppPhase `json:"phase,omitempty"`

	// ValidationMessage holds the reason when phase is Invalid.
	// +optional
	ValidationMessage string `json:"validationMessage,omitempty"`

	// DeploymentName is the owned Deployment (type=service).
	// +optional
	DeploymentName string `json:"deploymentName,omitempty"`

	// CodeSourceDeployedVersion is the artifact version the Deployment last
	// rolled to (type=service with codeSource).
	// +optional
	CodeSourceDeployedVersion string `json:"codeSourceDeployedVersion,omitempty"`

	// Executions lists recent job executions, newest first (capped).
	// +optional
	Executions []WeaveAppExecution `json:"executions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=fa
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// WeaveApp is a single service or job with all of its Kubernetes objects owned
// by this one resource: delete it and everything it created goes with it.
type WeaveApp struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WeaveAppSpec   `json:"spec,omitempty"`
	Status WeaveAppStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WeaveAppList contains a list of WeaveApp.
type WeaveAppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WeaveApp `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WeaveApp{}, &WeaveAppList{})
}
