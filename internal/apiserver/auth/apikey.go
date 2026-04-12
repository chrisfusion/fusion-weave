// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// apiKeyLabelKey is the label that marks a Secret as an API key.
	apiKeyLabelKey = "fusion-platform.io/api-key"
	// apiKeyRoleAnnotation holds the role for this API key.
	apiKeyRoleAnnotation = "fusion-platform.io/role"
	// apiKeyDataField is the Secret data key containing the token value.
	apiKeyDataField = "token"
)

// APIKeyValidator looks up API keys stored as Kubernetes Secrets labelled
// with fusion-platform.io/api-key=true. The token is stored as plaintext
// under the "token" key; a SHA-256 hash of the incoming token is compared
// against a SHA-256 hash of the stored value so the raw token is never
// held in memory longer than needed.
type APIKeyValidator struct {
	client    client.Client
	namespace string
}

func newAPIKeyValidator(c client.Client, namespace string) *APIKeyValidator {
	return &APIKeyValidator{client: c, namespace: namespace}
}

// Validate returns a Result when the token matches a known API key Secret,
// nil when no match is found, or an error for infrastructure failures.
func (v *APIKeyValidator) Validate(ctx context.Context, token string) (*Result, error) {
	var secrets corev1.SecretList
	if err := v.client.List(ctx, &secrets,
		client.InNamespace(v.namespace),
		client.MatchingLabels{apiKeyLabelKey: "true"},
	); err != nil {
		return nil, fmt.Errorf("list API key secrets: %w", err)
	}

	incomingHash := sha256sum(token)

	for _, s := range secrets.Items {
		stored, ok := s.Data[apiKeyDataField]
		if !ok {
			continue
		}
		if sha256sum(string(stored)) != incomingHash {
			continue
		}
		role := Role(s.Annotations[apiKeyRoleAnnotation])
		if !validRole(role) {
			role = RoleViewer
		}
		return &Result{
			Principal: "apikey/" + s.Name,
			Role:      role,
		}, nil
	}
	return nil, nil
}

func sha256sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func validRole(r Role) bool {
	return r == RoleViewer || r == RoleEditor || r == RoleAdmin
}
