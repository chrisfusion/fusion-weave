// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/chrisfusion/fusion-shared/listquery"
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

var appListSpec = listquery.Spec{SortKeys: []string{"name", "created", "lastExecution"}, DefaultSort: "name"}

var appFields = objectFields(nil, map[string]func(a, b *weavev1alpha1.WeaveApp) int{
	"lastExecution": func(a, b *weavev1alpha1.WeaveApp) int {
		return listquery.CompareTime(lastExecutionStart(a), lastExecutionStart(b))
	},
})

// List supports the shared list parameters plus type, phase, codeSource and running.
func (h *AppHandler) List(w http.ResponseWriter, r *http.Request) {
	p, opts, ok := h.parseList(w, r, appListSpec)
	if !ok {
		return
	}
	q := r.URL.Query()
	appTypes, errT := listquery.OneOf(q, "type", enumStrings(weavev1alpha1.WeaveAppTypeService, weavev1alpha1.WeaveAppTypeJob)...)
	phases, errP := listquery.OneOf(q, "phase", enumStrings(weavev1alpha1.WeaveAppPhasePending, weavev1alpha1.WeaveAppPhaseReady,
		weavev1alpha1.WeaveAppPhaseRunning, weavev1alpha1.WeaveAppPhaseInvalid)...)
	codeSource, errC := listquery.Bool(q, "codeSource")
	running, errR := listquery.Bool(q, "running")
	if badParam(w, errT, errP, errC, errR) {
		return
	}

	var list weavev1alpha1.WeaveAppList
	if err := h.client.List(r.Context(), &list, opts...); err != nil {
		internalError(w, r, err, "kind", "WeaveApp")
		return
	}
	page := listquery.Apply(list.Items, p, appFields, func(a *weavev1alpha1.WeaveApp) bool {
		return appTypes.Match(string(a.Spec.Type)) && phases.Match(string(a.Status.Phase)) &&
			boolMatch(codeSource, a.Spec.CodeSource != nil) && boolMatch(running, anyExecutionRunning(a))
	})
	list.Items = page.Items
	setPage(&list.ListMeta, page.Continue, page.Remaining)
	writeJSON(w, http.StatusOK, list)
}

var executionListSpec = listquery.Spec{SortKeys: []string{"name", "started", "completed", "entrypoint"}, DefaultSort: "-started"}

// executionFields: name is the execution's Job name, the time window applies to its start.
var executionFields = listquery.Fields[weavev1alpha1.WeaveAppExecution]{
	Name:   func(e *weavev1alpha1.WeaveAppExecution) string { return e.JobName },
	Time:   func(e *weavev1alpha1.WeaveAppExecution) time.Time { return timeOf(e.StartTime) },
	Labels: func(*weavev1alpha1.WeaveAppExecution) map[string]string { return nil },
	Sort: map[string]func(a, b *weavev1alpha1.WeaveAppExecution) int{
		"name": func(a, b *weavev1alpha1.WeaveAppExecution) int { return strings.Compare(a.JobName, b.JobName) },
		"started": func(a, b *weavev1alpha1.WeaveAppExecution) int {
			return listquery.CompareTime(timeOf(a.StartTime), timeOf(b.StartTime))
		},
		"completed": func(a, b *weavev1alpha1.WeaveAppExecution) int {
			return listquery.CompareTime(timeOf(a.CompletionTime), timeOf(b.CompletionTime))
		},
		"entrypoint": func(a, b *weavev1alpha1.WeaveAppExecution) int { return strings.Compare(a.Entrypoint, b.Entrypoint) },
	},
}

// Executions lists a job app's execution history (status.executions, the last 20) with the
// shared list parameters (source does not apply) plus entrypoint and phase.
func (h *AppHandler) Executions(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	p, _, ok := h.parseList(w, r, executionListSpec)
	if !ok {
		return
	}
	q := r.URL.Query()
	phases, err := listquery.OneOf(q, "phase", enumStrings(weavev1alpha1.WeaveAppExecutionRunning,
		weavev1alpha1.WeaveAppExecutionSucceeded, weavev1alpha1.WeaveAppExecutionFailed)...)
	if badParam(w, err) {
		return
	}
	entrypoints := listquery.Any(q, "entrypoint")

	var app weavev1alpha1.WeaveApp
	if err := h.client.Get(r.Context(), types.NamespacedName{Namespace: h.namespace, Name: name}, &app); err != nil {
		handleGetErr(w, r, err)
		return
	}
	page := listquery.Apply(app.Status.Executions, p, executionFields, func(e *weavev1alpha1.WeaveAppExecution) bool {
		return phases.Match(string(e.Phase)) && entrypoints.Match(e.Entrypoint)
	})
	writeJSON(w, http.StatusOK, newPagedList(page))
}

// lastExecutionStart is the start of the app's most recent execution (zero if none).
func lastExecutionStart(a *weavev1alpha1.WeaveApp) time.Time {
	var last time.Time
	for _, e := range a.Status.Executions {
		if t := timeOf(e.StartTime); t.After(last) {
			last = t
		}
	}
	return last
}

// anyExecutionRunning reports whether any entrypoint of the app is running.
func anyExecutionRunning(a *weavev1alpha1.WeaveApp) bool {
	for _, e := range a.Status.Executions {
		if e.Phase == weavev1alpha1.WeaveAppExecutionRunning {
			return true
		}
	}
	return false
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
