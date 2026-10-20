// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"fusion-platform.io/fusion-weave/internal/deploybuilder"
)

// isDeploymentAvailable returns true when the Deployment has the Available
// condition set to True — meaning the minimum number of pods is ready.
func isDeploymentAvailable(d *appsv1.Deployment) bool {
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// upsertService creates desired or patches ports and type on the existing Service.
func upsertService(ctx context.Context, c client.Client, desired *corev1.Service) error {
	var existing corev1.Service
	err := c.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing)
	if errors.IsNotFound(err) {
		if createErr := c.Create(ctx, desired); createErr != nil && !errors.IsAlreadyExists(createErr) {
			return createErr
		}
		return nil
	}
	if err != nil {
		return err
	}
	patch := client.MergeFrom(existing.DeepCopy())
	existing.Spec.Ports = desired.Spec.Ports
	existing.Spec.Type = desired.Spec.Type
	return c.Patch(ctx, &existing, patch)
}

// upsertIngress creates desired or replaces the spec of the existing Ingress.
func upsertIngress(ctx context.Context, c client.Client, desired *networkingv1.Ingress) error {
	var existing networkingv1.Ingress
	err := c.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing)
	if errors.IsNotFound(err) {
		if createErr := c.Create(ctx, desired); createErr != nil && !errors.IsAlreadyExists(createErr) {
			return createErr
		}
		return nil
	}
	if err != nil {
		return err
	}
	patch := client.MergeFrom(existing.DeepCopy())
	existing.Spec = desired.Spec
	// Additive only: changed instance-wide ingress.annotations/labels reach
	// existing Ingresses, annotations added by other tools are left alone.
	existing.Annotations = mergeStringMaps(existing.Annotations, desired.Annotations)
	existing.Labels = mergeStringMaps(existing.Labels, desired.Labels)
	return c.Patch(ctx, &existing, patch)
}

func mergeStringMaps(base, overlay map[string]string) map[string]string {
	if len(overlay) == 0 {
		return base
	}
	if base == nil {
		base = make(map[string]string, len(overlay))
	}
	for k, v := range overlay {
		base[k] = v
	}
	return base
}

// restartDeploymentForVersion triggers a rolling restart of the named
// Deployment after its code-source artifact moved to version, and keeps
// WEAVE_VERSION in the new pods accurate.
func restartDeploymentForVersion(ctx context.Context, c client.Client, ns, name, version string) error {
	var deploy appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &deploy); err != nil {
		return fmt.Errorf("get deployment for code reload: %w", err)
	}
	patch := client.MergeFrom(deploy.DeepCopy())
	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = map[string]string{}
	}
	deploy.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().UTC().Format(time.RFC3339)
	deploy.Spec.Template.Annotations["fusion-platform.io/code-source-version"] = version
	deploybuilder.UpdateVersionEnvVar(deploy.Spec.Template.Spec.Containers, version)
	if err := c.Patch(ctx, &deploy, patch); err != nil {
		return fmt.Errorf("patch deployment for code reload: %w", err)
	}
	return nil
}
