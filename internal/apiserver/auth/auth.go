// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package auth implements API key, OIDC, and ServiceAccount authentication.
package auth

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Role mirrors apiserver.Role to avoid a circular import.
type Role string

const (
	RoleViewer Role = "viewer"
	RoleEditor Role = "editor"
	RoleAdmin  Role = "admin"
)

// Config holds configuration for all auth validators.
type Config struct {
	Namespace  string
	Client     client.Client
	KubeClient kubernetes.Interface

	APIKeyEnabled bool
	OIDCEnabled   bool
	OIDCIssuerURL string
	OIDCClientID  string
	OIDCRoleClaim string
	SAAuthEnabled bool
	// AllowUnauthenticated skips auth entirely (cluster-internal mode).
	AllowUnauthenticated bool
}

// Result is returned by a successful authentication.
type Result struct {
	// Principal is a human-readable identity string for logging.
	Principal string
	// Role is the access level granted to this principal.
	Role Role
}

// Authenticator validates a bearer token and returns the caller's identity and role.
type Authenticator struct {
	cfg    Config
	apiKey *APIKeyValidator
	oidc   *OIDCValidator
	sa     *SAValidator
}

// New builds an Authenticator from config. Returns an error if OIDC is enabled
// but the issuer URL is unreachable (JWKS discovery).
func New(ctx context.Context, cfg Config) (*Authenticator, error) {
	if cfg.AllowUnauthenticated {
		ctrl.Log.WithName("auth").Info("WARNING: AllowUnauthenticated is enabled — all callers receive admin access; do not use in production")
	}
	a := &Authenticator{cfg: cfg}

	if cfg.APIKeyEnabled {
		a.apiKey = newAPIKeyValidator(cfg.Client, cfg.Namespace)
	}
	if cfg.OIDCEnabled {
		v, err := newOIDCValidator(ctx, cfg.OIDCIssuerURL, cfg.OIDCClientID, cfg.OIDCRoleClaim)
		if err != nil {
			return nil, fmt.Errorf("OIDC setup: %w", err)
		}
		a.oidc = v
	}
	if cfg.SAAuthEnabled {
		a.sa = newSAValidator(cfg.KubeClient, cfg.Client, cfg.Namespace)
	}
	return a, nil
}

// Authenticate tries each enabled validator in order: API key → OIDC → SA.
// Returns (nil, nil) when AllowUnauthenticated is set.
// Returns a non-nil error only for internal failures; a missing/invalid token
// returns (nil, nil) to let the middleware decide whether to 401.
func (a *Authenticator) Authenticate(ctx context.Context, authHeader string) (*Result, error) {
	if a.cfg.AllowUnauthenticated {
		return &Result{Principal: "unauthenticated", Role: RoleAdmin}, nil
	}

	token := extractBearer(authHeader)
	if token == "" {
		return nil, nil
	}

	if a.apiKey != nil {
		if r, err := a.apiKey.Validate(ctx, token); err != nil {
			return nil, err
		} else if r != nil {
			return r, nil
		}
	}

	if a.oidc != nil {
		if r, err := a.oidc.Validate(ctx, token); err != nil {
			return nil, err
		} else if r != nil {
			return r, nil
		}
	}

	if a.sa != nil {
		if r, err := a.sa.Validate(ctx, token); err != nil {
			return nil, err
		} else if r != nil {
			return r, nil
		}
	}

	return nil, nil
}

// extractBearer returns the token from a "Bearer <token>" Authorization header.
// Returns "" if the header is absent or does not have the Bearer prefix.
func extractBearer(header string) string {
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(header, "Bearer ")
}
