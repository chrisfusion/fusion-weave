// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"cmp"
	"encoding/json"
	"net/http"
	"time"

	"github.com/chrisfusion/fusion-shared/listquery"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/imagepolicy"
)

// RunHandler handles CRUD for WeaveRun.
type RunHandler struct {
	base
	allowedImagePrefixes []string
}

func NewRunHandler(c client.Client, namespace string, allowedImagePrefixes []string) ResourceHandler {
	return &RunHandler{base: base{client: c, namespace: namespace}, allowedImagePrefixes: allowedImagePrefixes}
}

// validateImageOverrides rejects a run whose image overrides are malformed:
// duplicate steps, untagged/:latest images, or images outside the allowlist.
// Chain-dependent checks (unknown step, chain-owned deploy step) are enforced by
// the operator. PATCH bypasses this check by design (raw merge patch); the
// operator re-validates before applying any image.
func (h *RunHandler) validateImageOverrides(run *weavev1alpha1.WeaveRun) error {
	return imagepolicy.ValidateList(run.Spec.ImageOverrides, h.allowedImagePrefixes)
}

var runListSpec = listquery.Spec{SortKeys: []string{"name", "created", "started", "completed", "duration"}, DefaultSort: "-created"}

// runFields: the since/until window applies to the start time, falling back to creation
// for runs that have not started.
func runFields(now time.Time) listquery.Fields[weavev1alpha1.WeaveRun] {
	return objectFields(runTime, map[string]func(a, b *weavev1alpha1.WeaveRun) int{
		"started": func(a, b *weavev1alpha1.WeaveRun) int {
			return listquery.CompareTime(timeOf(a.Status.StartTime), timeOf(b.Status.StartTime))
		},
		"completed": func(a, b *weavev1alpha1.WeaveRun) int {
			return listquery.CompareTime(timeOf(a.Status.CompletionTime), timeOf(b.Status.CompletionTime))
		},
		"duration": func(a, b *weavev1alpha1.WeaveRun) int {
			return cmp.Compare(runDuration(a, now), runDuration(b, now))
		},
	})
}

// runFilter holds the run-specific list parameters, shared by List and Facets.
type runFilter struct {
	phases, chains, triggers listquery.Set
}

func parseRunFilter(w http.ResponseWriter, r *http.Request) (runFilter, bool) {
	q := r.URL.Query()
	phases, err := listquery.OneOf(q, "phase", enumStrings(weavev1alpha1.RunPhasePending, weavev1alpha1.RunPhaseRunning,
		weavev1alpha1.RunPhaseSucceeded, weavev1alpha1.RunPhaseFailed, weavev1alpha1.RunPhaseStopped)...)
	if badParam(w, err) {
		return runFilter{}, false
	}
	return runFilter{phases: phases, chains: listquery.Any(q, "chain"), triggers: listquery.Any(q, "trigger")}, true
}

func (f runFilter) match(run *weavev1alpha1.WeaveRun) bool {
	return f.phases.Match(runPhase(run)) && f.chains.Match(run.Spec.ChainRef.Name) && f.triggers.Match(runTrigger(run))
}

// List supports the shared list parameters plus phase, chain and trigger.
func (h *RunHandler) List(w http.ResponseWriter, r *http.Request) {
	p, opts, ok := h.parseList(w, r, runListSpec)
	if !ok {
		return
	}
	f, ok := parseRunFilter(w, r)
	if !ok {
		return
	}
	var list weavev1alpha1.WeaveRunList
	if err := h.client.List(r.Context(), &list, opts...); err != nil {
		internalError(w, r, err, "kind", "WeaveRun")
		return
	}
	page := listquery.Apply(list.Items, p, runFields(time.Now()), f.match)
	list.Items = page.Items
	setPage(&list.ListMeta, page.Continue, page.Remaining)
	writeJSON(w, http.StatusOK, list)
}

// runFacets counts the runs matching a filter, for the filter drop-downs of a GUI.
type runFacets struct {
	Total   int            `json:"total"`
	Phase   map[string]int `json:"phase"`
	Chain   map[string]int `json:"chain"`
	Trigger map[string]int `json:"trigger"`
	Source  map[string]int `json:"source"`
}

// Facets counts runs per phase, chain, trigger and source. It takes the same filters as
// List (sort, limit and continue are accepted and ignored).
func (h *RunHandler) Facets(w http.ResponseWriter, r *http.Request) {
	p, opts, ok := h.parseList(w, r, runListSpec)
	if !ok {
		return
	}
	f, ok := parseRunFilter(w, r)
	if !ok {
		return
	}
	var list weavev1alpha1.WeaveRunList
	if err := h.client.List(r.Context(), &list, opts...); err != nil {
		internalError(w, r, err, "kind", "WeaveRun")
		return
	}
	runs := listquery.Filter(list.Items, p, runFields(time.Now()), f.match)
	writeJSON(w, http.StatusOK, runFacets{
		Total:   len(runs),
		Phase:   listquery.Count(runs, func(run *weavev1alpha1.WeaveRun) []string { return []string{runPhase(run)} }),
		Chain:   listquery.Count(runs, func(run *weavev1alpha1.WeaveRun) []string { return []string{run.Spec.ChainRef.Name} }),
		Trigger: listquery.Count(runs, func(run *weavev1alpha1.WeaveRun) []string { return []string{runTrigger(run)} }),
		Source:  listquery.Count(runs, func(run *weavev1alpha1.WeaveRun) []string { return listquery.Sources(run.Labels) }),
	})
}

// runPhase is the run's phase; a run the operator has not picked up yet counts as Pending.
func runPhase(run *weavev1alpha1.WeaveRun) string {
	if run.Status.Phase == "" {
		return string(weavev1alpha1.RunPhasePending)
	}
	return string(run.Status.Phase)
}

// runTrigger is the name of the trigger that created the run ("" for manual runs).
func runTrigger(run *weavev1alpha1.WeaveRun) string {
	if run.Spec.TriggerRef == nil {
		return ""
	}
	return run.Spec.TriggerRef.Name
}

// runTime is the start time, or the creation time for runs that have not started.
func runTime(run *weavev1alpha1.WeaveRun) time.Time {
	if run.Status.StartTime != nil {
		return run.Status.StartTime.Time
	}
	return run.CreationTimestamp.Time
}

// runDuration is completion minus start; still-running runs count up to now, unstarted runs are 0.
func runDuration(run *weavev1alpha1.WeaveRun, now time.Time) time.Duration {
	if run.Status.StartTime == nil {
		return 0
	}
	end := now
	if run.Status.CompletionTime != nil {
		end = run.Status.CompletionTime.Time
	}
	return end.Sub(run.Status.StartTime.Time)
}

func (h *RunHandler) Create(w http.ResponseWriter, r *http.Request) {
	var obj weavev1alpha1.WeaveRun
	if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := h.validateImageOverrides(&obj); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	obj.Namespace = h.namespace
	defaultManagedByManual(&obj)
	if err := h.client.Create(r.Context(), &obj); err != nil {
		if errors.IsAlreadyExists(err) {
			writeError(w, http.StatusConflict, "resource already exists")
			return
		}
		internalError(w, r, err, "kind", "WeaveRun", "name", obj.Name)
		return
	}
	writeJSON(w, http.StatusCreated, obj)
}

func (h *RunHandler) Get(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	var obj weavev1alpha1.WeaveRun
	if err := h.client.Get(r.Context(), types.NamespacedName{Namespace: h.namespace, Name: name}, &obj); err != nil {
		handleGetErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func (h *RunHandler) Update(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	var obj weavev1alpha1.WeaveRun
	if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := h.validateImageOverrides(&obj); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	obj.Name = name
	obj.Namespace = h.namespace
	if err := h.client.Update(r.Context(), &obj); err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "resource not found")
			return
		}
		internalError(w, r, err, "kind", "WeaveRun", "name", name)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func (h *RunHandler) Patch(w http.ResponseWriter, r *http.Request) {
	h.mergePatch(w, r, &weavev1alpha1.WeaveRun{})
}

func (h *RunHandler) Delete(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	obj := &weavev1alpha1.WeaveRun{}
	obj.Name = name
	obj.Namespace = h.namespace
	if err := h.client.Delete(r.Context(), obj); err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "resource not found")
			return
		}
		internalError(w, r, err, "kind", "WeaveRun", "name", name)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Stop patches status.phase = Stopped via the status subresource, triggering the
// operator's deploy-cleanup finalizer while keeping the run object in history.
func (h *RunHandler) Stop(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	var obj weavev1alpha1.WeaveRun
	if err := h.client.Get(r.Context(), types.NamespacedName{Namespace: h.namespace, Name: name}, &obj); err != nil {
		handleGetErr(w, r, err)
		return
	}
	if isTerminalPhase(obj.Status.Phase) {
		writeError(w, http.StatusConflict, "run is already in a terminal state")
		return
	}
	patch := client.MergeFrom(obj.DeepCopy())
	obj.Status.Phase = weavev1alpha1.RunPhaseStopped
	if err := h.client.Status().Patch(r.Context(), &obj, patch); err != nil {
		internalError(w, r, err, "kind", "WeaveRun", "name", name)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

// setImageRequest is the body of POST /runs/{name}/image.
type setImageRequest struct {
	StepName        string            `json:"stepName"`
	Image           string            `json:"image"`
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`
}

// SetImage upserts one entry of spec.imageOverrides on a non-terminal run. The
// image is validated against the allowlist here (unlike a raw PATCH), then the
// operator rolls a Deployed run-owned deploy step to it. The write is
// optimistic-locked so concurrent edits of other overrides are not lost.
func (h *RunHandler) SetImage(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	var req setImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.StepName == "" {
		writeError(w, http.StatusBadRequest, "stepName is required")
		return
	}
	if err := imagepolicy.Validate(req.Image, h.allowedImagePrefixes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var obj weavev1alpha1.WeaveRun
	if err := h.client.Get(r.Context(), types.NamespacedName{Namespace: h.namespace, Name: name}, &obj); err != nil {
		handleGetErr(w, r, err)
		return
	}
	if isTerminalPhase(obj.Status.Phase) {
		writeError(w, http.StatusConflict, "run is already in a terminal state")
		return
	}
	patch := client.MergeFromWithOptions(obj.DeepCopy(), client.MergeFromWithOptimisticLock{})
	entry := weavev1alpha1.WeaveRunImageOverride{StepName: req.StepName, Image: req.Image, ImagePullPolicy: req.ImagePullPolicy}
	found := false
	for i := range obj.Spec.ImageOverrides {
		if obj.Spec.ImageOverrides[i].StepName == req.StepName {
			obj.Spec.ImageOverrides[i] = entry
			found = true
		}
	}
	if !found {
		obj.Spec.ImageOverrides = append(obj.Spec.ImageOverrides, entry)
	}
	if err := h.client.Patch(r.Context(), &obj, patch); err != nil {
		if errors.IsConflict(err) {
			writeError(w, http.StatusConflict, "run was modified concurrently, retry")
			return
		}
		internalError(w, r, err, "kind", "WeaveRun", "name", name)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func isTerminalPhase(p weavev1alpha1.WeaveRunPhase) bool {
	return p == weavev1alpha1.RunPhaseSucceeded ||
		p == weavev1alpha1.RunPhaseFailed ||
		p == weavev1alpha1.RunPhaseStopped
}
