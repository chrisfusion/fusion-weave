// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/monitoring/logsink"
)

// LogResponse is the JSON body returned by the step logs endpoint.
type LogResponse struct {
	RunName  string   `json:"runName"`
	StepName string   `json:"stepName"`
	PodName  string   `json:"podName"`
	Lines    []string `json:"lines"`
}

// LogsHandler serves GET /monitor/v1/runs/{name}/steps/{step}/logs.
type LogsHandler struct{ Base }

func NewLogsHandler(b Base) *LogsHandler { return &LogsHandler{b} }

// Get handles GET /monitor/v1/runs/{name}/steps/{step}/logs.
// It resolves the pod from the step's batch/v1 Job, fetches the last N log
// lines as a snapshot, caches the result, and publishes it to the configured
// log sink asynchronously.
func (h *LogsHandler) Get(w http.ResponseWriter, r *http.Request) {
	runName := nameFromURL(w, r)
	if runName == "" {
		return
	}
	stepName := paramFromURL(w, r, "step")
	if stepName == "" {
		return
	}

	key := "run:logs:" + runName + ":" + stepName
	if h.cacheGet(w, key) {
		return
	}

	// Resolve the job name from the WeaveRun step status.
	var run weavev1alpha1.WeaveRun
	if err := h.Client.Get(r.Context(),
		client.ObjectKey{Namespace: h.Namespace, Name: runName}, &run,
	); err != nil {
		handleGetErr(w, r, err)
		return
	}

	var jobName string
	for _, s := range run.Status.Steps {
		if s.Name == stepName && s.JobRef != nil {
			jobName = s.JobRef.Name
			break
		}
	}
	if jobName == "" {
		writeError(w, http.StatusNotFound, "step not found or has no associated job")
		return
	}

	// List pods for the job using Kubernetes' native job-name label.
	podList, err := h.KubeClient.CoreV1().Pods(h.Namespace).List(r.Context(), metav1.ListOptions{
		LabelSelector: "job-name=" + jobName,
	})
	if err != nil {
		internalError(w, r, err)
		return
	}
	if len(podList.Items) == 0 {
		writeError(w, http.StatusNotFound, "no pods found for step")
		return
	}

	// Pick the most recently created pod.
	sort.Slice(podList.Items, func(i, j int) bool {
		return podList.Items[i].CreationTimestamp.After(
			podList.Items[j].CreationTimestamp.Time,
		)
	})
	pod := podList.Items[0]

	// Fetch the log snapshot with a bounded timeout.
	tailLines := int64(h.MaxLogLines)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	stream, err := h.KubeClient.CoreV1().Pods(h.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		Container: "job", // jobbuilder always names the container "job"
		TailLines: &tailLines,
	}).Stream(ctx)
	if err != nil {
		internalError(w, r, err)
		return
	}
	defer stream.Close()

	// Read with a 4 MiB safety cap.
	raw, err := io.ReadAll(io.LimitReader(stream, 4<<20))
	if err != nil {
		internalError(w, r, err)
		return
	}

	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = []string{}
	}

	resp := LogResponse{
		RunName:  runName,
		StepName: stepName,
		PodName:  pod.Name,
		Lines:    lines,
	}
	h.Cache.Set(key, resp)

	// Publish to the sink asynchronously — fire-and-forget.
	// Use context.Background() so the publish is not cancelled when the HTTP
	// response returns.
	go func() {
		snap := logsink.LogSnapshot{
			RunName:   runName,
			StepName:  stepName,
			PodName:   pod.Name,
			Lines:     lines,
			FetchedAt: time.Now(),
		}
		_ = h.Sink.Publish(context.Background(), snap)
	}()

	writeJSON(w, http.StatusOK, resp)
}
