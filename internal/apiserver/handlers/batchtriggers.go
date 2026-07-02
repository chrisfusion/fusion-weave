// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"encoding/json"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/trigger"
)

const batchJobsKey = "jobs.yaml"

// batchTriggerRequest is the body for Create and Update.
type batchTriggerRequest struct {
	Name     string                            `json:"name"`
	ChainRef corev1.LocalObjectReference       `json:"chainRef"`
	Jobs     string                            `json:"jobs"`
}

// resumeRequest is the body for the Resume action.
type resumeRequest struct {
	Jobs string `json:"jobs"`
}

// validateResponse is the body returned by the Validate action.
type validateResponse struct {
	Valid  bool                      `json:"valid"`
	Errors []trigger.ValidationError `json:"errors,omitempty"`
}

// BatchTriggerHandler handles CRUD and lifecycle actions for BatchCron WeaveTriggers.
type BatchTriggerHandler struct{ base }

func NewBatchTriggerHandler(c client.Client, namespace string) *BatchTriggerHandler {
	return &BatchTriggerHandler{base{client: c, namespace: namespace}}
}

func (h *BatchTriggerHandler) List(w http.ResponseWriter, r *http.Request) {
	var all weavev1alpha1.WeaveTriggerList
	if err := h.client.List(r.Context(), &all, client.InNamespace(h.namespace)); err != nil {
		internalError(w, r, err, "kind", "WeaveTrigger/BatchCron")
		return
	}
	// Filter to BatchCron type in-process; no field indexer needed.
	result := all
	result.Items = result.Items[:0]
	for _, item := range all.Items {
		if item.Spec.Type == weavev1alpha1.TriggerBatchCron {
			result.Items = append(result.Items, item)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *BatchTriggerHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req batchTriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.ChainRef.Name == "" {
		writeError(w, http.StatusBadRequest, "chainRef.name is required")
		return
	}
	if req.Jobs == "" {
		writeError(w, http.StatusBadRequest, "jobs is required")
		return
	}

	// Validate the YAML before touching Kubernetes.
	_, valErrs := trigger.ParseBatchJobs(req.Jobs)
	if len(valErrs) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, validateResponse{Valid: false, Errors: valErrs})
		return
	}

	cmName := batchCMName(req.Name)

	// Create the WeaveTrigger first so we can use its UID for the ConfigMap owner ref.
	ft := &weavev1alpha1.WeaveTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: h.namespace,
		},
		Spec: weavev1alpha1.WeaveTriggerSpec{
			ChainRef: req.ChainRef,
			Type:     weavev1alpha1.TriggerBatchCron,
			BatchCron: &weavev1alpha1.WeaveBatchCronConfig{
				JobsConfigMapRef: corev1.LocalObjectReference{Name: cmName},
			},
		},
	}
	if err := h.client.Create(r.Context(), ft); err != nil {
		if errors.IsAlreadyExists(err) {
			writeError(w, http.StatusConflict, "resource already exists")
			return
		}
		internalError(w, r, err, "kind", "WeaveTrigger", "name", req.Name)
		return
	}

	// Create the ConfigMap owned by the trigger so GC cascades on trigger deletion.
	// If ConfigMap creation fails, best-effort delete the trigger to avoid a stuck state.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: h.namespace,
			Labels: map[string]string{
				"fusion-platform.io/batch-trigger": req.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(ft, weavev1alpha1.GroupVersion.WithKind("WeaveTrigger")),
			},
		},
		Data: map[string]string{batchJobsKey: req.Jobs},
	}
	if err := h.client.Create(r.Context(), cm); err != nil {
		_ = h.client.Delete(r.Context(), ft)
		internalError(w, r, err, "kind", "ConfigMap", "name", cmName)
		return
	}

	writeJSON(w, http.StatusCreated, ft)
}

func (h *BatchTriggerHandler) Get(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	var obj weavev1alpha1.WeaveTrigger
	if err := h.client.Get(r.Context(), types.NamespacedName{Namespace: h.namespace, Name: name}, &obj); err != nil {
		handleGetErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

// Update replaces the jobs YAML in the ConfigMap and optionally updates other spec fields.
func (h *BatchTriggerHandler) Update(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	var req batchTriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Jobs == "" {
		writeError(w, http.StatusBadRequest, "jobs is required")
		return
	}

	_, valErrs := trigger.ParseBatchJobs(req.Jobs)
	if len(valErrs) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, validateResponse{Valid: false, Errors: valErrs})
		return
	}

	if err := h.updateJobsCM(r, name, req.Jobs); err != nil {
		internalError(w, r, err, "kind", "ConfigMap", "name", batchCMName(name))
		return
	}

	var obj weavev1alpha1.WeaveTrigger
	if err := h.client.Get(r.Context(), types.NamespacedName{Namespace: h.namespace, Name: name}, &obj); err != nil {
		handleGetErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func (h *BatchTriggerHandler) Patch(w http.ResponseWriter, r *http.Request) {
	h.mergePatch(w, r, &weavev1alpha1.WeaveTrigger{})
}

func (h *BatchTriggerHandler) Delete(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	obj := &weavev1alpha1.WeaveTrigger{}
	obj.Name = name
	obj.Namespace = h.namespace
	if err := h.client.Delete(r.Context(), obj); err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "resource not found")
			return
		}
		internalError(w, r, err, "kind", "WeaveTrigger", "name", name)
		return
	}
	// ConfigMap is GC'd automatically via OwnerReference.
	w.WriteHeader(http.StatusNoContent)
}

// Stop sets spec.paused=true, suspending all batch cron scheduling.
func (h *BatchTriggerHandler) Stop(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	var obj weavev1alpha1.WeaveTrigger
	if err := h.client.Get(r.Context(), types.NamespacedName{Namespace: h.namespace, Name: name}, &obj); err != nil {
		handleGetErr(w, r, err)
		return
	}
	if obj.Spec.Paused {
		writeError(w, http.StatusConflict, "trigger is already paused")
		return
	}
	patch := client.MergeFrom(obj.DeepCopy())
	obj.Spec.Paused = true
	if err := h.client.Patch(r.Context(), &obj, patch); err != nil {
		internalError(w, r, err, "kind", "WeaveTrigger", "name", name)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

// Resume uploads a new jobs YAML and sets spec.paused=false to resume scheduling.
func (h *BatchTriggerHandler) Resume(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	var req resumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Jobs == "" {
		writeError(w, http.StatusBadRequest, "jobs is required")
		return
	}

	_, valErrs := trigger.ParseBatchJobs(req.Jobs)
	if len(valErrs) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, validateResponse{Valid: false, Errors: valErrs})
		return
	}

	if err := h.updateJobsCM(r, name, req.Jobs); err != nil {
		internalError(w, r, err, "kind", "ConfigMap", "name", batchCMName(name))
		return
	}

	var obj weavev1alpha1.WeaveTrigger
	if err := h.client.Get(r.Context(), types.NamespacedName{Namespace: h.namespace, Name: name}, &obj); err != nil {
		handleGetErr(w, r, err)
		return
	}
	patch := client.MergeFrom(obj.DeepCopy())
	obj.Spec.Paused = false
	if err := h.client.Patch(r.Context(), &obj, patch); err != nil {
		internalError(w, r, err, "kind", "WeaveTrigger", "name", name)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

// Validate parses the submitted jobs YAML and returns line-level errors without touching Kubernetes.
func (h *BatchTriggerHandler) Validate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Jobs string `json:"jobs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Jobs == "" {
		writeError(w, http.StatusBadRequest, "jobs is required")
		return
	}

	_, errs := trigger.ParseBatchJobs(body.Jobs)
	writeJSON(w, http.StatusOK, validateResponse{
		Valid:  len(errs) == 0,
		Errors: errs,
	})
}

// updateJobsCM replaces jobs.yaml in the batch trigger's ConfigMap.
func (h *BatchTriggerHandler) updateJobsCM(r *http.Request, triggerName, jobs string) error {
	cmName := batchCMName(triggerName)
	var cm corev1.ConfigMap
	if err := h.client.Get(r.Context(), types.NamespacedName{Namespace: h.namespace, Name: cmName}, &cm); err != nil {
		return err
	}
	patch := client.MergeFrom(cm.DeepCopy())
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[batchJobsKey] = jobs
	return h.client.Patch(r.Context(), &cm, patch)
}

func batchCMName(triggerName string) string {
	return "batchtrigger-" + triggerName
}
