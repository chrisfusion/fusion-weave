// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"encoding/json"
	"net/http"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/imagepolicy"
)

// AppHandler handles CRUD for WeaveApp. Create/Update reject images outside the
// allowlist (400); PATCH, kubectl and Flux bypass this, so the operator
// re-validates and marks the app Invalid instead of running it.
type AppHandler struct {
	base
	allowedImagePrefixes []string
}

func NewAppHandler(c client.Client, namespace string, allowedImagePrefixes []string) ResourceHandler {
	return &AppHandler{base: base{client: c, namespace: namespace}, allowedImagePrefixes: allowedImagePrefixes}
}

func (h *AppHandler) List(w http.ResponseWriter, r *http.Request) {
	var list weavev1alpha1.WeaveAppList
	if err := h.client.List(r.Context(), &list, client.InNamespace(h.namespace)); err != nil {
		internalError(w, r, err, "kind", "WeaveApp")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *AppHandler) Create(w http.ResponseWriter, r *http.Request) {
	var obj weavev1alpha1.WeaveApp
	if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := imagepolicy.ValidateApp(obj.Spec.Image, h.allowedImagePrefixes); err != nil {
		writeError(w, http.StatusBadRequest, "spec.image: "+err.Error())
		return
	}
	if cs := obj.Spec.CodeSource; cs != nil && cs.LoaderImage != "" {
		if err := imagepolicy.ValidateApp(cs.LoaderImage, h.allowedImagePrefixes); err != nil {
			writeError(w, http.StatusBadRequest, "spec.codeSource.loaderImage: "+err.Error())
			return
		}
	}
	obj.Namespace = h.namespace
	defaultManagedByManual(&obj)
	if err := h.client.Create(r.Context(), &obj); err != nil {
		if errors.IsAlreadyExists(err) {
			writeError(w, http.StatusConflict, "resource already exists")
			return
		}
		internalError(w, r, err, "kind", "WeaveApp", "name", obj.Name)
		return
	}
	writeJSON(w, http.StatusCreated, obj)
}

func (h *AppHandler) Get(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	var obj weavev1alpha1.WeaveApp
	if err := h.client.Get(r.Context(), types.NamespacedName{Namespace: h.namespace, Name: name}, &obj); err != nil {
		handleGetErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func (h *AppHandler) Update(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	var obj weavev1alpha1.WeaveApp
	if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := imagepolicy.ValidateApp(obj.Spec.Image, h.allowedImagePrefixes); err != nil {
		writeError(w, http.StatusBadRequest, "spec.image: "+err.Error())
		return
	}
	if cs := obj.Spec.CodeSource; cs != nil && cs.LoaderImage != "" {
		if err := imagepolicy.ValidateApp(cs.LoaderImage, h.allowedImagePrefixes); err != nil {
			writeError(w, http.StatusBadRequest, "spec.codeSource.loaderImage: "+err.Error())
			return
		}
	}
	obj.Name = name
	obj.Namespace = h.namespace
	if err := h.client.Update(r.Context(), &obj); err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "resource not found")
			return
		}
		internalError(w, r, err, "kind", "WeaveApp", "name", name)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func (h *AppHandler) Patch(w http.ResponseWriter, r *http.Request) {
	h.mergePatch(w, r, &weavev1alpha1.WeaveApp{})
}

// fireRequest is the body of POST /apps/{name}/fire.
type fireRequest struct {
	Entrypoint string `json:"entrypoint"`
}

// Fire launches one entrypoint of a job app once by setting the fire annotation
// the operator consumes. Unlike a raw PATCH it only touches that annotation, so
// it can be granted separately from write access (BFF weave:apps:fire), and it
// answers right away for a service app, an unknown entrypoint (400) or an
// execution that is still running (409) instead of the operator silently
// skipping the fire.
func (h *AppHandler) Fire(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	var req fireRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Entrypoint == "" {
		writeError(w, http.StatusBadRequest, "entrypoint is required")
		return
	}
	var obj weavev1alpha1.WeaveApp
	if err := h.client.Get(r.Context(), types.NamespacedName{Namespace: h.namespace, Name: name}, &obj); err != nil {
		handleGetErr(w, r, err)
		return
	}
	if obj.Spec.Type != weavev1alpha1.WeaveAppTypeJob {
		writeError(w, http.StatusBadRequest, "only job apps have entrypoints to fire")
		return
	}
	if !obj.HasEntrypoint(req.Entrypoint) {
		writeError(w, http.StatusBadRequest, "unknown entrypoint "+req.Entrypoint)
		return
	}
	if obj.HasRunningExecution(req.Entrypoint) {
		writeError(w, http.StatusConflict, "previous execution of "+req.Entrypoint+" is still running")
		return
	}
	patch := client.MergeFrom(obj.DeepCopy())
	if obj.Annotations == nil {
		obj.Annotations = map[string]string{}
	}
	obj.Annotations[weavev1alpha1.WeaveAppFireAnnotation] = req.Entrypoint
	if err := h.client.Patch(r.Context(), &obj, patch); err != nil {
		internalError(w, r, err, "kind", "WeaveApp", "name", name)
		return
	}
	writeJSON(w, http.StatusAccepted, obj)
}

func (h *AppHandler) Delete(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	obj := &weavev1alpha1.WeaveApp{}
	obj.Name = name
	obj.Namespace = h.namespace
	if err := h.client.Delete(r.Context(), obj); err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "resource not found")
			return
		}
		internalError(w, r, err, "kind", "WeaveApp", "name", name)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
