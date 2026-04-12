// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"net/http"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// JobsHandler serves job endpoints under /monitor/v1/runs/{name}/jobs.
type JobsHandler struct{ Base }

func NewJobsHandler(b Base) *JobsHandler { return &JobsHandler{b} }

// List handles GET /monitor/v1/runs/{name}/jobs — returns all batch/v1 Jobs
// created for the named WeaveRun (label selector: fusion-platform.io/run=<name>).
func (h *JobsHandler) List(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	key := "run:jobs:" + name
	if h.cacheGet(w, key) {
		return
	}

	var jobList batchv1.JobList
	if err := h.Client.List(r.Context(), &jobList,
		client.InNamespace(h.Namespace),
		client.MatchingLabels{"fusion-platform.io/run": name},
	); err != nil {
		internalError(w, r, err)
		return
	}

	h.Cache.Set(key, jobList.Items)
	writeJSON(w, http.StatusOK, jobList.Items)
}

// Get handles GET /monitor/v1/runs/{name}/jobs/{jobName}.
func (h *JobsHandler) Get(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	jobName := paramFromURL(w, r, "jobName")
	if jobName == "" {
		return
	}
	key := "run:job:" + name + ":" + jobName
	if h.cacheGet(w, key) {
		return
	}

	var job batchv1.Job
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: jobName}, &job,
	); err != nil {
		handleGetErr(w, r, err)
		return
	}

	h.Cache.Set(key, job)
	writeJSON(w, http.StatusOK, job)
}
