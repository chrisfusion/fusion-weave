// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// FireRequest is sent from the webhook handler to the WeaveTrigger controller.
type FireRequest struct {
	// TriggerNamespace and TriggerName identify the WeaveTrigger to activate.
	TriggerNamespace string
	TriggerName      string
	// ParameterOverrides are env vars parsed from the webhook request body.
	ParameterOverrides []corev1.EnvVar
}

// TokenLookup is a function the webhook server calls to resolve a bearer token
// for a given trigger. Returning "" means no token is required.
type TokenLookup func(ctx context.Context, namespace, triggerName string) (string, error)

// WebhookServer is a plain HTTP server that accepts trigger POST requests.
// It implements controller-runtime's Runnable interface so it can be added
// to the manager with mgr.Add().
type WebhookServer struct {
	Addr        string
	FireCh      chan<- FireRequest
	TokenLookup TokenLookup

	mu       sync.RWMutex
	routes   map[string]routeEntry // path -> routeEntry
	server   *http.Server
}

type routeEntry struct {
	namespace   string
	triggerName string
}

// NewWebhookServer creates a WebhookServer listening on the given address.
func NewWebhookServer(addr string, fireCh chan<- FireRequest, lookup TokenLookup) *WebhookServer {
	return &WebhookServer{
		Addr:        addr,
		FireCh:      fireCh,
		TokenLookup: lookup,
		routes:      make(map[string]routeEntry),
	}
}

// Register maps an HTTP path to a WeaveTrigger so the server knows which
// trigger to fire when that path receives a POST.
func (w *WebhookServer) Register(path, namespace, triggerName string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.routes[path] = routeEntry{namespace: namespace, triggerName: triggerName}
}

// Unregister removes the route for the given path.
func (w *WebhookServer) Unregister(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.routes, path)
}

// Start implements controller-runtime Runnable. It blocks until ctx is done.
func (w *WebhookServer) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("webhook-server")
	mux := http.NewServeMux()
	mux.HandleFunc("/", w.handle)

	w.server = &http.Server{
		Addr:    w.Addr,
		Handler: mux,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("webhook server listening", "addr", w.Addr)
		if err := w.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return w.server.Shutdown(shutCtx)
	case err := <-errCh:
		return fmt.Errorf("webhook server error: %w", err)
	}
}

func (w *WebhookServer) handle(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.mu.RLock()
	entry, ok := w.routes[r.URL.Path]
	w.mu.RUnlock()
	if !ok {
		http.Error(rw, "not found", http.StatusNotFound)
		return
	}

	// Token validation.
	if w.TokenLookup != nil {
		expected, err := w.TokenLookup(r.Context(), entry.namespace, entry.triggerName)
		if err != nil {
			http.Error(rw, "internal error", http.StatusInternalServerError)
			return
		}
		if expected != "" {
			bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if bearer != expected {
				http.Error(rw, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
	}

	// Parse optional JSON body as env var overrides.
	var overrides []corev1.EnvVar
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&overrides); err != nil {
			http.Error(rw, "invalid body: expected JSON array of {name,value}", http.StatusBadRequest)
			return
		}
	}

	w.FireCh <- FireRequest{
		TriggerNamespace:   entry.namespace,
		TriggerName:        entry.triggerName,
		ParameterOverrides: overrides,
	}

	rw.WriteHeader(http.StatusAccepted)
}
