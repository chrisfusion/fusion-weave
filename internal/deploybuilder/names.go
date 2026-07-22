// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package deploybuilder constructs apps/v1 Deployment, Service, and Ingress
// objects from WeaveServiceTemplate + WeaveChain step metadata.
package deploybuilder

import (
	"fmt"

	"fusion-platform.io/fusion-weave/internal/codesource"
)

// stepResourceName builds the <prefix>-<stepName> name shared by a deploy
// step's Deployment, Service, and Ingress, truncated to 63 bytes — Service
// names must fit that limit (Kubernetes derives selector/endpoint labels from
// it), and Deployment/Ingress use the same value so all three stay linked for
// a given step rather than only Service silently diverging on long names.
func stepResourceName(prefix, stepName string) string {
	return codesource.TruncateK8sName(fmt.Sprintf("%s-%s", prefix, stepName), 63)
}

// DeploymentName returns the stable chain-owned Deployment name for a deploy step.
// Format: <chainName>-<stepName>
// The name is stable across runs so rolling updates target the same Deployment.
func DeploymentName(chainName, stepName string) string {
	return stepResourceName(chainName, stepName)
}

// ServiceName returns the stable chain-owned Service name for a deploy step.
func ServiceName(chainName, stepName string) string {
	return stepResourceName(chainName, stepName)
}

// IngressName returns the stable chain-owned Ingress name for a deploy step.
func IngressName(chainName, stepName string) string {
	return stepResourceName(chainName, stepName)
}

// RunDeploymentName returns the run-owned Deployment name for a step override.
// Format: <runName>-<stepName>. Each WeaveRun instance gets its own Deployment.
func RunDeploymentName(runName, stepName string) string {
	return stepResourceName(runName, stepName)
}

// RunServiceName returns the run-owned Service name for a step override.
func RunServiceName(runName, stepName string) string {
	return stepResourceName(runName, stepName)
}

// RunIngressName returns the run-owned Ingress name for a step override.
func RunIngressName(runName, stepName string) string {
	return stepResourceName(runName, stepName)
}

// IngressHost joins a user-supplied ingress rule name with the operator-wide
// host suffix (ingress.hostSuffix / INGRESS_HOST_SUFFIX) to produce the final
// Ingress hostname. Users only ever control the leftmost label; the domain is
// always the operator's fixed suffix, so a template or run can never point an
// Ingress at an arbitrary external hostname.
func IngressHost(name, hostSuffix string) string {
	if hostSuffix == "" {
		return name
	}
	return fmt.Sprintf("%s.%s", name, hostSuffix)
}
