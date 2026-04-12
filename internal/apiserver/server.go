// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package apiserver implements the fusion-weave REST API service.
package apiserver

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"fusion-platform.io/fusion-weave/internal/apiserver/auth"
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
}

// Server is the REST API HTTP server.
type Server struct {
	cfg        Config
	httpServer *http.Server
	client     client.Client
	kubeClient kubernetes.Interface
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

	router := newRouter(cfg, c, authCfg)

	s.httpServer = &http.Server{
		Addr:         cfg.Addr,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	return s, nil
}

// Start begins serving requests and blocks until ctx is cancelled.
// Implements sigs.k8s.io/controller-runtime/pkg/manager.Runnable.
func (s *Server) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("apiserver")
	logger.Info("starting API server", "addr", s.cfg.Addr)

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
		logger.Info("API server stopped")
		return nil
	case err := <-errCh:
		return fmt.Errorf("API server error: %w", err)
	}
}
