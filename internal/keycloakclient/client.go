// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package keycloakclient provides a minimal HTTP client for minting OIDC
// access tokens from Keycloak via the client_credentials grant. It is used by
// the operator to authenticate Job-kind step containers to an external
// service on their behalf, without exposing the Keycloak client secret to the
// job's own code.
package keycloakclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ErrTokenRequestFailed is returned when Keycloak rejects the client_credentials
// grant (bad client id/secret, disabled client, etc.).
var ErrTokenRequestFailed = errors.New("keycloakclient: token request failed")

// Token is the parsed subset of a Keycloak client_credentials response.
type Token struct {
	AccessToken string
	ExpiresIn   int // seconds, as reported by Keycloak
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// MintClientCredentialsToken performs the OAuth2 client_credentials grant
// against tokenURL (the full Keycloak token endpoint, e.g.
// "https://keycloak.example.com/realms/fusion/protocol/openid-connect/token").
func MintClientCredentialsToken(ctx context.Context, tokenURL, clientID, clientSecret string) (*Token, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("keycloakclient: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keycloakclient: POST token endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: unexpected status %d", ErrTokenRequestFailed, resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("keycloakclient: decode token response: %w", err)
	}
	return &Token{AccessToken: tr.AccessToken, ExpiresIn: tr.ExpiresIn}, nil
}
