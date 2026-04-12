// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package handlers implements HTTP handlers for the fusion-weave monitoring API.
package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"fusion-platform.io/fusion-weave/internal/monitoring/cache"
	"fusion-platform.io/fusion-weave/internal/monitoring/logsink"
)

// Base holds the shared dependencies for all monitoring handlers.
type Base struct {
	Client      client.Client
	KubeClient  kubernetes.Interface
	Namespace   string
	Cache       *cache.TTLCache[string, any]
	Sink        logsink.Sink
	MaxLogLines int
}

// NewBase constructs a Base from the given dependencies.
func NewBase(
	c client.Client,
	kc kubernetes.Interface,
	ns string,
	ch *cache.TTLCache[string, any],
	sink logsink.Sink,
	maxLogLines int,
) Base {
	return Base{
		Client:      c,
		KubeClient:  kc,
		Namespace:   ns,
		Cache:       ch,
		Sink:        sink,
		MaxLogLines: maxLogLines,
	}
}

// ---- shared HTTP helpers ----

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Code: status, Message: msg})
}

func internalError(w http.ResponseWriter, r *http.Request, err error) {
	log.FromContext(r.Context()).WithName("monitor").Error(err, "kubernetes operation failed")
	writeError(w, http.StatusInternalServerError, "internal server error")
}

// cacheGet checks the cache for key. On a hit it writes the JSON response and
// returns true so the caller can immediately return. On a miss it increments
// the miss counter and returns false.
func (b *Base) cacheGet(w http.ResponseWriter, key string) bool {
	if v, ok := b.Cache.Get(key); ok {
		cacheHitsTotal.Inc()
		writeJSON(w, http.StatusOK, v)
		return true
	}
	cacheMissesTotal.Inc()
	return false
}

func nameFromURL(w http.ResponseWriter, r *http.Request) string {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing name parameter")
	}
	return name
}

func paramFromURL(w http.ResponseWriter, r *http.Request, param string) string {
	v := chi.URLParam(r, param)
	if v == "" {
		writeError(w, http.StatusBadRequest, "missing "+param+" parameter")
	}
	return v
}

func handleGetErr(w http.ResponseWriter, r *http.Request, err error) {
	if k8serrors.IsNotFound(err) {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	internalError(w, r, err)
}
