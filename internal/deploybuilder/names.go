// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package deploybuilder constructs apps/v1 Deployment, Service, and Ingress
// objects from WeaveServiceTemplate + WeaveChain step metadata.
package deploybuilder

import "fmt"

// DeploymentName returns the stable Deployment name for a deploy step.
// Format: <chainName>-<stepName>
// The name is stable across runs so rolling updates target the same Deployment.
func DeploymentName(chainName, stepName string) string {
	return fmt.Sprintf("%s-%s", chainName, stepName)
}

// ServiceName returns the stable Service name for a deploy step.
func ServiceName(chainName, stepName string) string {
	return fmt.Sprintf("%s-%s", chainName, stepName)
}

// IngressName returns the stable Ingress name for a deploy step.
func IngressName(chainName, stepName string) string {
	return fmt.Sprintf("%s-%s", chainName, stepName)
}
