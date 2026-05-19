// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

type loggerKey struct{}

// WithLogger stores l on ctx so downstream handlers can retrieve it.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// LoggerFromCtx returns the per-request logger stored by Logging.
// Falls back to slog.Default() when the middleware was not applied.
func LoggerFromCtx(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Logging generates a request ID, stamps a per-request *slog.Logger with
// {request_id, method, path, client_ip}, stores it in the request context,
// and emits the access log line after the handler returns.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		b := make([]byte, 8)
		_, _ = rand.Read(b)
		reqID := hex.EncodeToString(b)

		logger := slog.Default().With(
			"request_id", reqID,
			"method", r.Method,
			"path", r.URL.Path,
			"client_ip", r.RemoteAddr,
		)

		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r.WithContext(WithLogger(r.Context(), logger)))

		logger.Info("request",
			"status", rw.status,
			"latency_ms", time.Since(start).Milliseconds(),
		)
	})
}
