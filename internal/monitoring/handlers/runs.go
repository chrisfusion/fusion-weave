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
// seenRuns tracks terminal runs whose duration has already been observed in the
// runDurationSeconds histogram, preventing double-counting across polling cycles.
type RunsHandler struct {
	Base
	seenRuns map[string]struct{}
}

func NewRunsHandler(b Base) *RunsHandler {
	return &RunsHandler{Base: b, seenRuns: make(map[string]struct{})}
}

// allRunPhases is the complete ordered set of WeaveRun phases.
var allRunPhases = []weavev1alpha1.WeaveRunPhase{
	weavev1alpha1.RunPhasePending,
	weavev1alpha1.RunPhaseRunning,
	weavev1alpha1.RunPhaseSucceeded,
	weavev1alpha1.RunPhaseFailed,
	weavev1alpha1.RunPhaseStopped,
}

// allStepPhases is the complete ordered set of WeaveRun step phases.
var allStepPhases = []weavev1alpha1.WeaveStepPhase{
	weavev1alpha1.StepPhasePending,
	weavev1alpha1.StepPhaseRunning,
	weavev1alpha1.StepPhaseSucceeded,
	weavev1alpha1.StepPhaseFailed,
	weavev1alpha1.StepPhaseSkipped,
	weavev1alpha1.StepPhaseRetrying,
}

// terminalRunPhases is the set of phases where a run no longer progresses.
var terminalRunPhases = map[weavev1alpha1.WeaveRunPhase]bool{
	weavev1alpha1.RunPhaseSucceeded: true,
	weavev1alpha1.RunPhaseFailed:    true,
	weavev1alpha1.RunPhaseStopped:   true,
}

// List handles GET /monitor/v1/runs — returns a summary slice.
// As a side effect it refreshes all Tier-1 and Tier-2 Prometheus metrics.
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

	// chain -> phase -> count for weave_runs_by_phase
	runPhaseCounts := map[string]map[string]float64{}
	// chain -> step-phase -> count for weave_step_phase_total
	stepPhaseCounts := map[string]map[string]float64{}
	// chain -> total retry count for weave_step_retry_count
	retryTotals := map[string]float64{}

	for i := range list.Items {
		run := &list.Items[i]
		chain := run.Spec.ChainRef.Name

		if runPhaseCounts[chain] == nil {
			runPhaseCounts[chain] = map[string]float64{}
		}
		if stepPhaseCounts[chain] == nil {
			stepPhaseCounts[chain] = map[string]float64{}
		}
		runPhaseCounts[chain][string(run.Status.Phase)]++

		// Observe run duration exactly once per terminal run.
		if terminalRunPhases[run.Status.Phase] {
			if _, seen := h.seenRuns[run.Name]; !seen {
				if run.Status.StartTime != nil && run.Status.CompletionTime != nil {
					d := run.Status.CompletionTime.Time.Sub(run.Status.StartTime.Time).Seconds()
					runDurationSeconds.WithLabelValues(chain, string(run.Status.Phase)).Observe(d)
				}
				h.seenRuns[run.Name] = struct{}{}
			}
		}

		failed := 0
		for _, s := range run.Status.Steps {
			if s.Phase == weavev1alpha1.StepPhaseFailed {
				failed++
			}
			stepPhaseCounts[chain][string(s.Phase)]++
			retryTotals[chain] += float64(s.RetryCount)
		}

		summaries = append(summaries, RunSummary{
			Name:           run.Name,
			Chain:          chain,
			Phase:          run.Status.Phase,
			StartTime:      run.Status.StartTime,
			CompletionTime: run.Status.CompletionTime,
			StepCount:      len(run.Status.Steps),
			FailedSteps:    failed,
			Message:        run.Status.Message,
		})
	}

	// Refresh weave_runs_by_phase{phase, chain}.
	runPhaseGauge.Reset()
	for chain, phases := range runPhaseCounts {
		for _, phase := range allRunPhases {
			runPhaseGauge.WithLabelValues(string(phase), chain).Set(phases[string(phase)])
		}
	}

	// Refresh weave_step_phase_total{chain, phase}.
	stepPhaseTotal.Reset()
	for chain, phases := range stepPhaseCounts {
		for _, phase := range allStepPhases {
			stepPhaseTotal.WithLabelValues(chain, string(phase)).Set(phases[string(phase)])
		}
	}

	// Refresh weave_step_retry_count{chain}.
	stepRetryCount.Reset()
	for chain, count := range retryTotals {
		stepRetryCount.WithLabelValues(chain).Set(count)
	}

	// Refresh Tier-2 chain-level metrics (valid, step count, deployment health).
	refreshChainMetrics(r.Context(), h.Client, h.Namespace)

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
