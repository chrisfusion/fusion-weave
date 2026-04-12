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
)

// JobTemplateHandler handles CRUD for WeaveJobTemplate.
type JobTemplateHandler struct{ base }

func NewJobTemplateHandler(c client.Client, namespace string) ResourceHandler {
	return &JobTemplateHandler{base{client: c, namespace: namespace}}
}

func (h *JobTemplateHandler) List(w http.ResponseWriter, r *http.Request) {
	var list weavev1alpha1.WeaveJobTemplateList
	if err := h.client.List(r.Context(), &list, client.InNamespace(h.namespace)); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *JobTemplateHandler) Create(w http.ResponseWriter, r *http.Request) {
	var obj weavev1alpha1.WeaveJobTemplate
	if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	obj.Namespace = h.namespace
	if err := h.client.Create(r.Context(), &obj); err != nil {
		if errors.IsAlreadyExists(err) {
			writeError(w, http.StatusConflict, "resource already exists")
			return
		}
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, obj)
}

func (h *JobTemplateHandler) Get(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	var obj weavev1alpha1.WeaveJobTemplate
	if err := h.client.Get(r.Context(), types.NamespacedName{Namespace: h.namespace, Name: name}, &obj); err != nil {
		handleGetErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func (h *JobTemplateHandler) Update(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	var obj weavev1alpha1.WeaveJobTemplate
	if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	obj.Name = name
	obj.Namespace = h.namespace
	if err := h.client.Update(r.Context(), &obj); err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "resource not found")
			return
		}
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func (h *JobTemplateHandler) Patch(w http.ResponseWriter, r *http.Request) {
	h.mergePatch(w, r, &weavev1alpha1.WeaveJobTemplate{})
}

func (h *JobTemplateHandler) Delete(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	obj := &weavev1alpha1.WeaveJobTemplate{}
	obj.Name = name
	obj.Namespace = h.namespace
	if err := h.client.Delete(r.Context(), obj); err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "resource not found")
			return
		}
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
