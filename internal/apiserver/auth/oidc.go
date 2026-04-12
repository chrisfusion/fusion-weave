// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package auth

import (
	"context"
	"fmt"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
)

// OIDCValidator verifies JWTs against an OIDC provider's JWKS endpoint.
// The role is read from a configurable claim in the token payload.
type OIDCValidator struct {
	verifier  *gooidc.IDTokenVerifier
	roleClaim string
}

func newOIDCValidator(ctx context.Context, issuerURL, clientID, roleClaim string) (*OIDCValidator, error) {
	provider, err := gooidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider %q: %w", issuerURL, err)
	}
	verifier := provider.Verifier(&gooidc.Config{ClientID: clientID})
	return &OIDCValidator{verifier: verifier, roleClaim: roleClaim}, nil
}

// Validate verifies the JWT and extracts the role claim.
// Returns nil (no error) when the token is simply invalid — the caller
// should try the next validator. Returns an error only for configuration
// or infrastructure failures.
func (v *OIDCValidator) Validate(ctx context.Context, token string) (*Result, error) {
	idToken, err := v.verifier.Verify(ctx, token)
	if err != nil {
		return nil, nil // invalid token — try next validator
	}

	var claims map[string]interface{}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("extract OIDC claims: %w", err)
	}

	role := RoleViewer
	if raw, ok := claims[v.roleClaim]; ok {
		if r := Role(fmt.Sprintf("%v", raw)); validRole(r) {
			role = r
		}
	}

	sub, _ := claims["sub"].(string)
	if sub == "" {
		sub = "oidc/unknown"
	}

	return &Result{
		Principal: "oidc/" + sub,
		Role:      role,
	}, nil
}
