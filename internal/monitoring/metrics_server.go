// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package monitoring

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// MetricsServer serves Prometheus metrics on a dedicated port with no authentication.
// It is intentionally minimal — no chi router, no middleware, just promhttp.Handler().
type MetricsServer struct {
	httpServer *http.Server
}

// NewMetricsServer creates a MetricsServer that will listen on addr (e.g. ":9091").
func NewMetricsServer(addr string) *MetricsServer {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	return &MetricsServer{
		httpServer: &http.Server{
			Addr:         addr,
			Handler:      mux,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  60 * time.Second,
		},
	}
}

// Start begins serving Prometheus metrics and blocks until ctx is cancelled.
func (s *MetricsServer) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("metrics-server")
	logger.Info("starting metrics server", "addr", s.httpServer.Addr)

	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error(err, "metrics server graceful shutdown failed")
		}
		logger.Info("metrics server stopped")
		return nil
	case err := <-errCh:
		return fmt.Errorf("metrics server error: %w", err)
	}
}
