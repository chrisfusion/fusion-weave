// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package handlers implements HTTP handlers for the fusion-weave REST API.
package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ResourceHandler is the interface the router expects for each CRD resource.
type ResourceHandler interface {
	List(w http.ResponseWriter, r *http.Request)
	Create(w http.ResponseWriter, r *http.Request)
	Get(w http.ResponseWriter, r *http.Request)
	Update(w http.ResponseWriter, r *http.Request)
	Patch(w http.ResponseWriter, r *http.Request)
	Delete(w http.ResponseWriter, r *http.Request)
}

// base holds the shared Kubernetes client and namespace used by all handlers.
type base struct {
	client    client.Client
	namespace string
}

// writeJSON encodes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a standard JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	type apiError struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	writeJSON(w, status, apiError{Code: status, Message: msg})
}

// internalError logs err server-side and returns a generic 500 to the caller,
// avoiding leakage of internal Kubernetes error details.
func internalError(w http.ResponseWriter, r *http.Request, err error) {
	log.FromContext(r.Context()).WithName("apiserver").Error(err, "kubernetes operation failed")
	writeError(w, http.StatusInternalServerError, "internal server error")
}

// nameFromURL returns the {name} path parameter or writes 400 and returns "".
func nameFromURL(w http.ResponseWriter, r *http.Request) string {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing name parameter")
	}
	return name
}

// handleGetErr translates a controller-runtime Get error into an HTTP response.
func handleGetErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.IsNotFound(err) {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	internalError(w, r, err)
}

// mergePatch applies a JSON Merge Patch from the request body to the named object.
func (b *base) mergePatch(w http.ResponseWriter, r *http.Request, obj client.Object) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	obj.SetName(name)
	obj.SetNamespace(b.namespace)

	var rawPatch map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&rawPatch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON patch: "+err.Error())
		return
	}
	patchBytes, err := json.Marshal(rawPatch)
	if err != nil {
		internalError(w, r, err)
		return
	}

	if err := b.client.Get(r.Context(), types.NamespacedName{Namespace: b.namespace, Name: name}, obj); err != nil {
		handleGetErr(w, r, err)
		return
	}
	if err := b.client.Patch(r.Context(), obj, client.RawPatch(types.MergePatchType, patchBytes)); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}
