// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package main

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

// lookupWebhookToken reads the bearer token for a WeaveTrigger from its
// referenced Secret. Returns "" if no SecretRef is configured.
func lookupWebhookToken(ctx context.Context, c client.Client, namespace, triggerName string) (string, error) {
	var ft weavev1alpha1.WeaveTrigger
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: triggerName}, &ft); err != nil {
		return "", fmt.Errorf("get trigger: %w", err)
	}
	if ft.Spec.Webhook == nil || ft.Spec.Webhook.SecretRef == nil {
		return "", nil
	}

	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      ft.Spec.Webhook.SecretRef.Name,
	}, &secret); err != nil {
		return "", fmt.Errorf("get webhook secret: %w", err)
	}

	token, ok := secret.Data["token"]
	if !ok {
		return "", fmt.Errorf("secret %q has no 'token' key", ft.Spec.Webhook.SecretRef.Name)
	}
	return string(token), nil
}
