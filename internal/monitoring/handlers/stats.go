// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

// RunStatsResponse is the aggregated statistics response.
type RunStatsResponse struct {
	Window        string  `json:"window"`
	Total         int     `json:"total"`
	Succeeded     int     `json:"succeeded"`
	Failed        int     `json:"failed"`
	Running       int     `json:"running"`
	Pending       int     `json:"pending"`
	Stopped       int     `json:"stopped"`
	SuccessRate   float64 `json:"successRate"`
	AvgDurationMs int64   `json:"avgDurationMs"`
	MinDurationMs int64   `json:"minDurationMs"`
	MaxDurationMs int64   `json:"maxDurationMs"`
}

// StatsHandler serves aggregated run statistics endpoints.
type StatsHandler struct{ Base }

func NewStatsHandler(b Base) *StatsHandler { return &StatsHandler{b} }

// RunStats handles GET /monitor/v1/stats/runs.
// Supports ?window= query param (e.g. "1h", "24h", "7d"). Default: "1h".
func (h *StatsHandler) RunStats(w http.ResponseWriter, r *http.Request) {
	window := r.URL.Query().Get("window")
	if window == "" {
		window = "1h"
	}
	key := "stats:runs:" + window
	if h.cacheGet(w, key) {
		return
	}

	var list weavev1alpha1.WeaveRunList
	if err := h.Client.List(r.Context(), &list, client.InNamespace(h.Namespace)); err != nil {
		internalError(w, r, err)
		return
	}

	cutoff := time.Now().Add(-parseWindow(window))
	resp := computeRunStats(list.Items, cutoff, window)
	h.Cache.Set(key, resp)
	writeJSON(w, http.StatusOK, resp)
}

// ChainStats handles GET /monitor/v1/stats/chains/{name}.
// Returns the same aggregated stats scoped to a single WeaveChain.
func (h *StatsHandler) ChainStats(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	window := r.URL.Query().Get("window")
	if window == "" {
		window = "1h"
	}
	key := "stats:chain:" + name + ":" + window
	if h.cacheGet(w, key) {
		return
	}

	// List all runs; filter by chain name in-process.
	// Field selectors for spec.* are not supported on CRDs without a server-side index.
	var list weavev1alpha1.WeaveRunList
	if err := h.Client.List(r.Context(), &list, client.InNamespace(h.Namespace)); err != nil {
		internalError(w, r, err)
		return
	}

	filtered := make([]weavev1alpha1.WeaveRun, 0, len(list.Items))
	for i := range list.Items {
		if list.Items[i].Spec.ChainRef.Name == name {
			filtered = append(filtered, list.Items[i])
		}
	}

	cutoff := time.Now().Add(-parseWindow(window))
	resp := computeRunStats(filtered, cutoff, window)
	h.Cache.Set(key, resp)
	writeJSON(w, http.StatusOK, resp)
}

// computeRunStats aggregates statistics over the given runs within the window.
func computeRunStats(runs []weavev1alpha1.WeaveRun, cutoff time.Time, window string) RunStatsResponse {
	resp := RunStatsResponse{Window: window}

	var totalDur time.Duration
	var minDur time.Duration = -1 // -1 = not yet set
	var maxDur time.Duration
	durCount := 0

	for i := range runs {
		run := &runs[i]
		if !inWindow(run, cutoff) {
			continue
		}
		resp.Total++
		switch run.Status.Phase {
		case weavev1alpha1.RunPhaseSucceeded:
			resp.Succeeded++
		case weavev1alpha1.RunPhaseFailed:
			resp.Failed++
		case weavev1alpha1.RunPhaseRunning:
			resp.Running++
		case weavev1alpha1.RunPhasePending:
			resp.Pending++
		case weavev1alpha1.RunPhaseStopped:
			resp.Stopped++
		}

		// Accumulate duration only for completed runs.
		if run.Status.StartTime != nil && run.Status.CompletionTime != nil {
			d := run.Status.CompletionTime.Time.Sub(run.Status.StartTime.Time)
			totalDur += d
			durCount++
			if minDur < 0 || d < minDur {
				minDur = d
			}
			if d > maxDur {
				maxDur = d
			}
		}
	}

	terminal := resp.Succeeded + resp.Failed + resp.Stopped
	if terminal > 0 {
		resp.SuccessRate = float64(resp.Succeeded) / float64(terminal)
	}
	if durCount > 0 {
		resp.AvgDurationMs = totalDur.Milliseconds() / int64(durCount)
		resp.MinDurationMs = minDur.Milliseconds()
		resp.MaxDurationMs = maxDur.Milliseconds()
	}
	return resp
}

// inWindow reports whether a run falls within the stats time window.
// Active runs are included only if they started within the window; runs that
// have been stuck for longer than the window are excluded to avoid skewing stats.
func inWindow(run *weavev1alpha1.WeaveRun, cutoff time.Time) bool {
	if run.Status.StartTime == nil {
		return false // no timing information yet
	}
	if run.Status.CompletionTime == nil {
		// Active: include only if started within the window.
		return run.Status.StartTime.Time.After(cutoff)
	}
	// Completed: include if either started or completed within the window.
	return run.Status.CompletionTime.Time.After(cutoff) ||
		run.Status.StartTime.Time.After(cutoff)
}

// parseWindow converts a window string (e.g. "1h", "30m", "7d") into a
// time.Duration. Supports the "d" day suffix in addition to standard Go
// duration syntax. Returns 1 hour on parse failure.
func parseWindow(w string) time.Duration {
	if strings.HasSuffix(w, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(w, "d")); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour
		}
	}
	if d, err := time.ParseDuration(w); err == nil && d > 0 {
		return d
	}
	return time.Hour
}
