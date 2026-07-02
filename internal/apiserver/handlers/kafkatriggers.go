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
)

// kafkaTriggerRequest is the body for Create and Update.
type kafkaTriggerRequest struct {
	Name     string                         `json:"name"`
	ChainRef corev1.LocalObjectReference    `json:"chainRef"`
	Kafka    weavev1alpha1.WeaveKafkaConfig `json:"kafka"`
}

// KafkaTriggerHandler handles CRUD for Kafka-type WeaveTriggers.
type KafkaTriggerHandler struct{ base }

func NewKafkaTriggerHandler(c client.Client, namespace string) *KafkaTriggerHandler {
	return &KafkaTriggerHandler{base{client: c, namespace: namespace}}
}

func (h *KafkaTriggerHandler) List(w http.ResponseWriter, r *http.Request) {
	var all weavev1alpha1.WeaveTriggerList
	if err := h.client.List(r.Context(), &all, client.InNamespace(h.namespace)); err != nil {
		internalError(w, r, err, "kind", "WeaveTrigger/Kafka")
		return
	}
	// Filter to Kafka type in-process; no field indexer needed.
	result := all
	result.Items = result.Items[:0]
	for _, item := range all.Items {
		if item.Spec.Type == weavev1alpha1.TriggerKafka {
			result.Items = append(result.Items, item)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *KafkaTriggerHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req kafkaTriggerRequest
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
	if len(req.Kafka.Brokers) == 0 {
		writeError(w, http.StatusBadRequest, "kafka.brokers is required")
		return
	}
	if req.Kafka.Topic == "" {
		writeError(w, http.StatusBadRequest, "kafka.topic is required")
		return
	}
	if req.Kafka.ConsumerGroup == "" {
		writeError(w, http.StatusBadRequest, "kafka.consumerGroup is required")
		return
	}

	ft := &weavev1alpha1.WeaveTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: h.namespace,
		},
		Spec: weavev1alpha1.WeaveTriggerSpec{
			ChainRef: req.ChainRef,
			Type:     weavev1alpha1.TriggerKafka,
			Kafka:    &req.Kafka,
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
	writeJSON(w, http.StatusCreated, ft)
}

func (h *KafkaTriggerHandler) Get(w http.ResponseWriter, r *http.Request) {
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

func (h *KafkaTriggerHandler) Update(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	var req kafkaTriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(req.Kafka.Brokers) == 0 {
		writeError(w, http.StatusBadRequest, "kafka.brokers is required")
		return
	}
	if req.Kafka.Topic == "" {
		writeError(w, http.StatusBadRequest, "kafka.topic is required")
		return
	}
	if req.Kafka.ConsumerGroup == "" {
		writeError(w, http.StatusBadRequest, "kafka.consumerGroup is required")
		return
	}

	var obj weavev1alpha1.WeaveTrigger
	if err := h.client.Get(r.Context(), types.NamespacedName{Namespace: h.namespace, Name: name}, &obj); err != nil {
		handleGetErr(w, r, err)
		return
	}
	patch := client.MergeFrom(obj.DeepCopy())
	obj.Spec.Kafka = &req.Kafka
	if err := h.client.Patch(r.Context(), &obj, patch); err != nil {
		internalError(w, r, err, "kind", "WeaveTrigger", "name", name)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func (h *KafkaTriggerHandler) Patch(w http.ResponseWriter, r *http.Request) {
	h.mergePatch(w, r, &weavev1alpha1.WeaveTrigger{})
}

func (h *KafkaTriggerHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
