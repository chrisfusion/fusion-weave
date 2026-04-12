// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package apiserver implements the fusion-weave REST API service.
package apiserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"fusion-platform.io/fusion-weave/internal/apiserver/auth"
	"fusion-platform.io/fusion-weave/internal/monitoring"
	"fusion-platform.io/fusion-weave/internal/monitoring/logsink"
)

// Config holds all configuration for the API server.
type Config struct {
	// Addr is the TCP address to listen on (e.g. ":8082").
	Addr string
	// Namespace is the Kubernetes namespace the operator manages.
	Namespace string

	// APIKeyEnabled enables API key authentication.
	APIKeyEnabled bool
	// OIDCEnabled enables OIDC JWT authentication.
	OIDCEnabled bool
	// OIDCIssuerURL is the OIDC provider's issuer URL (required when OIDCEnabled).
	OIDCIssuerURL string
	// OIDCClientID is the expected audience in the JWT (required when OIDCEnabled).
	OIDCClientID string
	// OIDCRoleClaim is the JWT claim name that carries the role (default: "fusion-weave-role").
	OIDCRoleClaim string
	// SAAuthEnabled enables Kubernetes ServiceAccount token authentication.
	SAAuthEnabled bool
	// AllowUnauthenticated skips all auth checks (cluster-internal mode).
	AllowUnauthenticated bool

	// MonitoringEnabled enables the /monitor/v1 routes.
	MonitoringEnabled bool
	// MetricsAddr is the TCP address for the Prometheus metrics server (e.g. ":9091").
	// Empty disables the metrics server.
	MetricsAddr string
	// MonitorCacheTTL is the TTL for monitoring in-memory cache entries.
	MonitorCacheTTL time.Duration
	// MonitorMaxLogLines is the maximum number of tail log lines returned per step.
	MonitorMaxLogLines int

	// KafkaEnabled enables the Kafka log sink.
	KafkaEnabled bool
	// KafkaBrokers is a comma-separated list of Kafka broker addresses.
	KafkaBrokers string
	// KafkaTopic is the Kafka topic for log snapshots.
	KafkaTopic string
}

// Server is the REST API HTTP server.
type Server struct {
	cfg           Config
	httpServer    *http.Server
	client        client.Client
	kubeClient    kubernetes.Interface
	sink          logsink.Sink
	metricsServer *monitoring.MetricsServer
}

// New creates a Server but does not start it.
func New(cfg Config, c client.Client, kc kubernetes.Interface) (*Server, error) {
	s := &Server{
		cfg:        cfg,
		client:     c,
		kubeClient: kc,
	}

	// Validate auth config.
	if !cfg.AllowUnauthenticated && !cfg.APIKeyEnabled && !cfg.OIDCEnabled && !cfg.SAAuthEnabled {
		return nil, fmt.Errorf("at least one auth mode must be enabled, or set AllowUnauthenticated=true")
	}

	roleClaim := cfg.OIDCRoleClaim
	if roleClaim == "" {
		roleClaim = "fusion-weave-role"
	}

	authCfg := auth.Config{
		Namespace:            cfg.Namespace,
		Client:               c,
		KubeClient:           kc,
		APIKeyEnabled:        cfg.APIKeyEnabled,
		OIDCEnabled:          cfg.OIDCEnabled,
		OIDCIssuerURL:        cfg.OIDCIssuerURL,
		OIDCClientID:         cfg.OIDCClientID,
		OIDCRoleClaim:        roleClaim,
		SAAuthEnabled:        cfg.SAAuthEnabled,
		AllowUnauthenticated: cfg.AllowUnauthenticated,
	}

	// Build log sink.
	s.sink = buildSink(cfg, log.Log.WithName("apiserver"))

	// Build monitoring config passed to the router.
	cacheTTL := cfg.MonitorCacheTTL
	if cacheTTL <= 0 {
		cacheTTL = 30 * time.Second
	}
	maxLines := cfg.MonitorMaxLogLines
	if maxLines <= 0 {
		maxLines = 100
	}
	monCfg := monitoring.Config{
		Namespace:   cfg.Namespace,
		Client:      c,
		KubeClient:  kc,
		CacheTTL:    cacheTTL,
		MaxLogLines: maxLines,
		Sink:        s.sink,
	}

	router := newRouter(cfg, c, authCfg, monCfg)
	s.httpServer = &http.Server{
		Addr:         cfg.Addr,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	if cfg.MetricsAddr != "" {
		s.metricsServer = monitoring.NewMetricsServer(cfg.MetricsAddr)
	}

	return s, nil
}

// buildSink constructs a KafkaSink when Kafka is configured, otherwise NoopSink.
func buildSink(cfg Config, logger logr.Logger) logsink.Sink {
	if cfg.KafkaEnabled && cfg.KafkaBrokers != "" {
		brokers := strings.Split(cfg.KafkaBrokers, ",")
		return logsink.NewKafkaSink(brokers, cfg.KafkaTopic, logger)
	}
	return logsink.NoopSink{}
}

// Start begins serving requests and blocks until ctx is cancelled.
// Implements sigs.k8s.io/controller-runtime/pkg/manager.Runnable.
func (s *Server) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("apiserver")
	logger.Info("starting API server", "addr", s.cfg.Addr)

	// Start metrics server on a separate goroutine if configured.
	if s.metricsServer != nil {
		go func() {
			if err := s.metricsServer.Start(ctx); err != nil {
				logger.Error(err, "metrics server error")
			}
		}()
	}

	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error(err, "graceful shutdown failed")
		}
		// Close the log sink if it supports it (KafkaSink does, NoopSink does not).
		if closer, ok := s.sink.(io.Closer); ok {
			if err := closer.Close(); err != nil {
				logger.Error(err, "log sink close failed")
			}
		}
		logger.Info("API server stopped")
		return nil
	case err := <-errCh:
		return fmt.Errorf("API server error: %w", err)
	}
}
