// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package auth

import (
	"context"
	"fmt"
	"strings"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// saRoleLabel is the label on a ServiceAccount that specifies its API role.
	saRoleLabel = "fusion-platform.io/role"
)

// SAValidator authenticates Kubernetes ServiceAccount bearer tokens using
// the TokenReview API. The role is read from the saRoleLabel on the
// ServiceAccount object; unknown or unlabelled accounts default to viewer.
type SAValidator struct {
	kubeClient kubernetes.Interface
	client     client.Client
	namespace  string
}

func newSAValidator(kc kubernetes.Interface, c client.Client, namespace string) *SAValidator {
	return &SAValidator{kubeClient: kc, client: c, namespace: namespace}
}

// Validate submits a TokenReview and, on success, looks up the ServiceAccount
// to read its role label.
func (v *SAValidator) Validate(ctx context.Context, token string) (*Result, error) {
	tr, err := v.kubeClient.AuthenticationV1().TokenReviews().Create(ctx,
		&authv1.TokenReview{
			Spec: authv1.TokenReviewSpec{Token: token},
		},
		metav1.CreateOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("TokenReview: %w", err)
	}
	if !tr.Status.Authenticated {
		return nil, nil
	}

	// Username format for SA tokens: system:serviceaccount:<namespace>:<name>
	saName := extractSAName(tr.Status.User.Username)
	principal := "sa/" + tr.Status.User.Username

	role := RoleViewer
	if saName != "" {
		var sa corev1.ServiceAccount
		key := client.ObjectKey{Namespace: v.namespace, Name: saName}
		if err := v.client.Get(ctx, key, &sa); err == nil {
			if r := Role(sa.Labels[saRoleLabel]); validRole(r) {
				role = r
			}
		}
	}

	return &Result{Principal: principal, Role: role}, nil
}

// extractSAName parses "system:serviceaccount:<ns>:<name>" and returns <name>.
func extractSAName(username string) string {
	parts := strings.Split(username, ":")
	if len(parts) == 4 && parts[0] == "system" && parts[1] == "serviceaccount" {
		return parts[3]
	}
	return ""
}

