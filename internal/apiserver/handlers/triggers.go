// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/chrisfusion/fusion-shared/listquery"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/imagepolicy"
)

// TriggerHandler handles CRUD for WeaveTrigger.
type TriggerHandler struct {
	base
	allowedImagePrefixes []string
}

func NewTriggerHandler(c client.Client, namespace string, allowedImagePrefixes []string) ResourceHandler {
	return &TriggerHandler{base: base{client: c, namespace: namespace}, allowedImagePrefixes: allowedImagePrefixes}
}

func (h *TriggerHandler) List(w http.ResponseWriter, r *http.Request) {
	h.listTriggers(w, r, "")
}

var triggerListSpec = listquery.Spec{SortKeys: []string{"name", "created", "lastSchedule"}, DefaultSort: "name"}

var triggerFields = objectFields(nil, map[string]func(a, b *weavev1alpha1.WeaveTrigger) int{
	"lastSchedule": func(a, b *weavev1alpha1.WeaveTrigger) int {
		return listquery.CompareTime(timeOf(a.Status.LastScheduleTime), timeOf(b.Status.LastScheduleTime))
	},
})

// listTriggers serves /triggers (only == "") and the typed /batchtriggers and /kafkatriggers
// lists. It supports the shared list parameters plus type, chain, paused, active and quarantined.
func (b *base) listTriggers(w http.ResponseWriter, r *http.Request, only weavev1alpha1.WeaveTriggerType) {
	p, opts, ok := b.parseList(w, r, triggerListSpec)
	if !ok {
		return
	}
	q := r.URL.Query()
	triggerTypes, errT := listquery.OneOf(q, "type", enumStrings(weavev1alpha1.TriggerOnDemand, weavev1alpha1.TriggerCron,
		weavev1alpha1.TriggerWebhook, weavev1alpha1.TriggerBatchCron, weavev1alpha1.TriggerKafka)...)
	paused, errP := listquery.Bool(q, "paused")
	active, errA := listquery.Bool(q, "active")
	quarantined, errQ := listquery.Bool(q, "quarantined")
	if badParam(w, errT, errP, errA, errQ) {
		return
	}
	chains := listquery.Any(q, "chain")

	var list weavev1alpha1.WeaveTriggerList
	if err := b.client.List(r.Context(), &list, opts...); err != nil {
		internalError(w, r, err, "kind", "WeaveTrigger", "type", string(only))
		return
	}
	page := listquery.Apply(list.Items, p, triggerFields, func(t *weavev1alpha1.WeaveTrigger) bool {
		return (only == "" || t.Spec.Type == only) && triggerTypes.Match(string(t.Spec.Type)) &&
			chains.Match(t.Spec.ChainRef.Name) && boolMatch(paused, t.Spec.Paused) &&
			boolMatch(active, t.Status.Active) && boolMatch(quarantined, t.Status.Quarantined)
	})
	list.Items = page.Items
	setPage(&list.ListMeta, page.Continue, page.Remaining)
	writeJSON(w, http.StatusOK, list)
}

func (h *TriggerHandler) Create(w http.ResponseWriter, r *http.Request) {
	var obj weavev1alpha1.WeaveTrigger
	if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := imagepolicy.ValidateList(obj.Spec.ImageOverrides, h.allowedImagePrefixes); err != nil {
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
		internalError(w, r, err, "kind", "WeaveTrigger", "name", obj.Name)
		return
	}
	writeJSON(w, http.StatusCreated, obj)
}

func (h *TriggerHandler) Get(w http.ResponseWriter, r *http.Request) {
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

func (h *TriggerHandler) Update(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	var obj weavev1alpha1.WeaveTrigger
	if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := imagepolicy.ValidateList(obj.Spec.ImageOverrides, h.allowedImagePrefixes); err != nil {
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
		internalError(w, r, err, "kind", "WeaveTrigger", "name", name)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func (h *TriggerHandler) Patch(w http.ResponseWriter, r *http.Request) {
	h.mergePatch(w, r, &weavev1alpha1.WeaveTrigger{})
}

func (h *TriggerHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	w.WriteHeader(http.StatusNoContent)
}
