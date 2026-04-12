// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"net/http"
	"regexp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fieldSelectorRe allows only characters that are valid in a Kubernetes field
// selector. This prevents cache-key pollution and blocks trivially malformed input.
var fieldSelectorRe = regexp.MustCompile(`^[a-zA-Z0-9./=!,_()\- ]{0,512}$`)

// EventsHandler serves Kubernetes event endpoints.
type EventsHandler struct{ Base }

func NewEventsHandler(b Base) *EventsHandler { return &EventsHandler{b} }

// ListForRun handles GET /monitor/v1/runs/{name}/events — returns all
// Kubernetes Events whose involvedObject is the named WeaveRun.
func (h *EventsHandler) ListForRun(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	key := "run:events:" + name
	if h.cacheGet(w, key) {
		return
	}

	eventList, err := h.KubeClient.CoreV1().Events(h.Namespace).List(r.Context(), metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + name + ",involvedObject.kind=WeaveRun",
	})
	if err != nil {
		internalError(w, r, err)
		return
	}

	h.Cache.Set(key, eventList.Items)
	writeJSON(w, http.StatusOK, eventList.Items)
}

// ListAll handles GET /monitor/v1/events — returns all Events in the managed
// namespace. An optional ?fieldSelector= query parameter is forwarded to the
// Kubernetes API.
func (h *EventsHandler) ListAll(w http.ResponseWriter, r *http.Request) {
	fs := r.URL.Query().Get("fieldSelector")
	if fs != "" && !fieldSelectorRe.MatchString(fs) {
		writeError(w, http.StatusBadRequest, "invalid fieldSelector")
		return
	}

	key := "events:all:" + fs
	if h.cacheGet(w, key) {
		return
	}

	opts := metav1.ListOptions{}
	if fs != "" {
		opts.FieldSelector = fs
	}

	eventList, err := h.KubeClient.CoreV1().Events(h.Namespace).List(r.Context(), opts)
	if err != nil {
		internalError(w, r, err)
		return
	}

	h.Cache.Set(key, eventList.Items)
	writeJSON(w, http.StatusOK, eventList.Items)
}
