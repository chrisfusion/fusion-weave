// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package security provides shared security configuration types used by the
// operator to apply consistent pod and container hardening across all workloads.
package security

import corev1 "k8s.io/api/core/v1"

// Defaults holds operator-wide security settings applied to every pod the
// operator creates — both batch Jobs (job steps) and Deployments (deploy steps).
// The zero value is safe: no security context or extra metadata is injected.
//
// Values are set once at operator startup (from WORKLOAD_SECURITY_DEFAULTS env
// var) and apply uniformly to all workload pods. For operator/API pod security,
// use the corresponding Helm podSecurityContext / containerSecurityContext values.
type Defaults struct {
	// PodAnnotations are merged into every workload PodTemplateSpec's annotations.
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`

	// PodLabels are merged into every workload PodTemplateSpec's labels.
	PodLabels map[string]string `json:"podLabels,omitempty"`

	// PodSecurityContext is applied at the pod level of every workload pod.
	// Supports all corev1.PodSecurityContext fields including seccompProfile.
	PodSecurityContext *corev1.PodSecurityContext `json:"podSecurityContext,omitempty"`

	// ContainerSecurityContext is applied to every workload container, including
	// init containers (e.g. code-loader).
	ContainerSecurityContext *corev1.SecurityContext `json:"containerSecurityContext,omitempty"`
}
