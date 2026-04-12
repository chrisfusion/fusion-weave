// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"net/http"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

// RunSummary is a projected, monitoring-focused view of a WeaveRun.
type RunSummary struct {
	Name           string                      `json:"name"`
	Chain          string                      `json:"chain"`
	Phase          weavev1alpha1.WeaveRunPhase `json:"phase"`
	StartTime      *metav1.Time               `json:"startTime,omitempty"`
	CompletionTime *metav1.Time               `json:"completionTime,omitempty"`
	StepCount      int                        `json:"stepCount"`
	FailedSteps    int                        `json:"failedSteps"`
	Message        string                     `json:"message,omitempty"`
}

// RunDetail combines a WeaveRun with its associated batch/v1 Jobs and Events.
type RunDetail struct {
	Run    weavev1alpha1.WeaveRun `json:"run"`
	Jobs   []batchv1.Job         `json:"jobs"`
	Events []corev1.Event        `json:"events"`
}

// RunsHandler serves GET /monitor/v1/runs and GET /monitor/v1/runs/{name}.
type RunsHandler struct{ Base }

func NewRunsHandler(b Base) *RunsHandler { return &RunsHandler{b} }

// List handles GET /monitor/v1/runs — returns a summary slice.
func (h *RunsHandler) List(w http.ResponseWriter, r *http.Request) {
	const key = "runs:list"
	if h.cacheGet(w, key) {
		return
	}

	var list weavev1alpha1.WeaveRunList
	if err := h.Client.List(r.Context(), &list, client.InNamespace(h.Namespace)); err != nil {
		internalError(w, r, err)
		return
	}

	summaries := make([]RunSummary, 0, len(list.Items))
	phaseCounts := map[string]float64{}
	for i := range list.Items {
		run := &list.Items[i]
		failed := 0
		for _, s := range run.Status.Steps {
			if s.Phase == weavev1alpha1.StepPhaseFailed {
				failed++
			}
		}
		summaries = append(summaries, RunSummary{
			Name:           run.Name,
			Chain:          run.Spec.ChainRef.Name,
			Phase:          run.Status.Phase,
			StartTime:      run.Status.StartTime,
			CompletionTime: run.Status.CompletionTime,
			StepCount:      len(run.Status.Steps),
			FailedSteps:    failed,
			Message:        run.Status.Message,
		})
		phaseCounts[string(run.Status.Phase)]++
	}

	// Update Prometheus gauge with fresh counts.
	for _, phase := range []weavev1alpha1.WeaveRunPhase{
		weavev1alpha1.RunPhasePending, weavev1alpha1.RunPhaseRunning,
		weavev1alpha1.RunPhaseSucceeded, weavev1alpha1.RunPhaseFailed,
		weavev1alpha1.RunPhaseStopped,
	} {
		runPhaseGauge.WithLabelValues(string(phase)).Set(phaseCounts[string(phase)])
	}

	h.Cache.Set(key, summaries)
	writeJSON(w, http.StatusOK, summaries)
}

// Get handles GET /monitor/v1/runs/{name} — returns a RunDetail.
func (h *RunsHandler) Get(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	key := "run:detail:" + name
	if h.cacheGet(w, key) {
		return
	}

	var run weavev1alpha1.WeaveRun
	if err := h.Client.Get(r.Context(), client.ObjectKey{Namespace: h.Namespace, Name: name}, &run); err != nil {
		handleGetErr(w, r, err)
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

	eventList, err := h.KubeClient.CoreV1().Events(h.Namespace).List(r.Context(), metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + name + ",involvedObject.kind=WeaveRun",
	})
	if err != nil {
		internalError(w, r, err)
		return
	}

	detail := RunDetail{
		Run:    run,
		Jobs:   jobList.Items,
		Events: eventList.Items,
	}
	h.Cache.Set(key, detail)
	writeJSON(w, http.StatusOK, detail)
}
