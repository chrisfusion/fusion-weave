// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"fusion-platform.io/fusion-weave/internal/apiserver/auth"
)

// Auth returns a middleware that authenticates each request using the
// configured validators. On success it stores the Role and Principal in
// the request context for the RBAC middleware to read.
func Auth(cfg auth.Config) func(http.Handler) http.Handler {
	// Build the authenticator once — OIDC discovery is network I/O so we
	// defer it to the first request and cache the result with sync.Once.
	var (
		once          sync.Once
		authenticator *auth.Authenticator
		initErr       error
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			once.Do(func() {
				authenticator, initErr = auth.New(context.Background(), cfg)
			})
			if initErr != nil {
				writeMiddlewareError(w, http.StatusServiceUnavailable, "auth service unavailable")
				return
			}

			result, err := authenticator.Authenticate(r.Context(), r.Header.Get("Authorization"))
			if err != nil {
				writeMiddlewareError(w, http.StatusInternalServerError, "authentication error")
				return
			}
			if result == nil {
				writeMiddlewareError(w, http.StatusUnauthorized, "unauthorized")
				return
			}

			ctx := context.WithValue(r.Context(), roleContextKey{}, result.Role)
			ctx = context.WithValue(ctx, principalContextKey{}, result.Principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RoleFromContext retrieves the authenticated role from the request context.
func RoleFromContext(ctx context.Context) auth.Role {
	if r, ok := ctx.Value(roleContextKey{}).(auth.Role); ok {
		return r
	}
	return ""
}

type roleContextKey struct{}
type principalContextKey struct{}

// writeMiddlewareError writes a JSON error response with the correct Content-Type.
// Used by all middleware that needs to short-circuit with an error.
func writeMiddlewareError(w http.ResponseWriter, status int, msg string) {
	type apiError struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{Code: status, Message: msg})
}
