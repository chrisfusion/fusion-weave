// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package middleware

import (
	"net/http"

	"fusion-platform.io/fusion-weave/internal/apiserver/auth"
)

// RBAC enforces verb-based access control based on the role stored in context
// by the Auth middleware.
//
//	viewer  → GET
//	editor  → GET, POST, PUT, PATCH
//	admin   → all methods including DELETE
func RBAC(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := RoleFromContext(r.Context())
		if !roleAllowed(role, r.Method) {
			writeMiddlewareError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func roleAllowed(role auth.Role, method string) bool {
	switch role {
	case auth.RoleAdmin:
		return true
	case auth.RoleEditor:
		return method != http.MethodDelete
	case auth.RoleViewer:
		return method == http.MethodGet
	}
	return false
}
