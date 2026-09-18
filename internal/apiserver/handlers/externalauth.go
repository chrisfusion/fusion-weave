// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"net/http"
	"strings"
)

// ExternalAuthOptions lists the deploy-time-allowlisted names available for
// WeaveExternalAuthRef, grouped by mode, so a GUI can populate a name picker
// for whichever mode the user selects.
type ExternalAuthOptions struct {
	ServiceAccounts []string `json:"serviceAccounts"`
	OIDCSecrets     []string `json:"oidcSecrets"`
}

// NewExternalAuthOptionsHandler returns a handler reporting the allowlists an
// operator configured via EXTERNAL_AUTH_SERVICE_ACCOUNTS/EXTERNAL_AUTH_OIDC_SECRETS
// (colon-separated). The lists are parsed once at startup and echoed as-is on
// every request — this mirrors how WeaveChainReconciler/WeaveRunReconciler
// treat them as a static allowlist, with no cluster lookups.
func NewExternalAuthOptionsHandler(serviceAccounts, oidcSecrets string) http.HandlerFunc {
	opts := ExternalAuthOptions{
		ServiceAccounts: splitAllowlist(serviceAccounts),
		OIDCSecrets:     splitAllowlist(oidcSecrets),
	}
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, opts)
	}
}

// splitAllowlist parses a colon-separated allowlist, trimming whitespace and
// dropping empty entries. Mirrors the parsing in cmd/main.go for the operator
// side of the same env vars. Always returns a non-nil slice so the JSON
// response is "[]" rather than "null" when unconfigured.
func splitAllowlist(v string) []string {
	out := []string{}
	for _, n := range strings.Split(v, ":") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}
