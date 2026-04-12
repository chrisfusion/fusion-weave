// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package middleware

import (
	"net/http"
	"runtime/debug"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Recovery catches panics, logs the stack trace, and returns a 500 response.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger := log.FromContext(r.Context()).WithName("apiserver")
				logger.Error(nil, "panic recovered", "panic", rec, "stack", string(debug.Stack()))
				writeMiddlewareError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
