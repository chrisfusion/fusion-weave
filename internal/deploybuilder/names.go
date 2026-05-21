// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package deploybuilder constructs apps/v1 Deployment, Service, and Ingress
// objects from WeaveServiceTemplate + WeaveChain step metadata.
package deploybuilder

import "fmt"

// DeploymentName returns the stable chain-owned Deployment name for a deploy step.
// Format: <chainName>-<stepName>
// The name is stable across runs so rolling updates target the same Deployment.
func DeploymentName(chainName, stepName string) string {
	return fmt.Sprintf("%s-%s", chainName, stepName)
}

// ServiceName returns the stable chain-owned Service name for a deploy step.
func ServiceName(chainName, stepName string) string {
	return fmt.Sprintf("%s-%s", chainName, stepName)
}

// IngressName returns the stable chain-owned Ingress name for a deploy step.
func IngressName(chainName, stepName string) string {
	return fmt.Sprintf("%s-%s", chainName, stepName)
}

// RunDeploymentName returns the run-owned Deployment name for a step override.
// Format: <runName>-<stepName>. Each WeaveRun instance gets its own Deployment.
func RunDeploymentName(runName, stepName string) string {
	return fmt.Sprintf("%s-%s", runName, stepName)
}

// RunServiceName returns the run-owned Service name for a step override.
func RunServiceName(runName, stepName string) string {
	return fmt.Sprintf("%s-%s", runName, stepName)
}

// RunIngressName returns the run-owned Ingress name for a step override.
func RunIngressName(runName, stepName string) string {
	return fmt.Sprintf("%s-%s", runName, stepName)
}
