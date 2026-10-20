// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"context"
	"errors"
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

	var jobName, deploymentName string
	for _, s := range run.Status.Steps {
		if s.Name == stepName {
			if s.JobRef != nil {
				jobName = s.JobRef.Name
			}
			if s.DeploymentRef != nil {
				deploymentName = s.DeploymentRef.Name
			}
			break
		}
	}

	var (
		pod           *corev1.Pod
		containerName string
		err           error
	)
	switch {
	case jobName != "":
		pod, err = h.podForJob(r.Context(), jobName)
		containerName = "job"
	case deploymentName != "":
		pod, containerName, err = h.podForDeployment(r.Context(), deploymentName)
	default:
		writeError(w, http.StatusNotFound, "step not found or has no associated job")
		return
	}
	if err != nil {
		h.writePodErr(w, r, err)
		return
	}

	lines, err := h.tailLogs(r.Context(), pod.Name, containerName)
	if err != nil {
		internalError(w, r, err)
		return
	}

	resp := LogResponse{
		RunName:  runName,
		StepName: stepName,
		PodName:  pod.Name,
		Lines:    lines,
	}
	h.Cache.Set(key, resp)

	h.publishSnapshot(logsink.LogSnapshot{
		RunName:   runName,
		StepName:  stepName,
		PodName:   pod.Name,
		Lines:     lines,
		FetchedAt: time.Now(),
	})

	writeJSON(w, http.StatusOK, resp)
}

// pickBestPod returns the Running pod with the latest creation timestamp,
// falling back to the most-recently created pod if none is Running.
func pickBestPod(pods []corev1.Pod) corev1.Pod {
	sort.Slice(pods, func(i, j int) bool {
		iRunning := pods[i].Status.Phase == corev1.PodRunning
		jRunning := pods[j].Status.Phase == corev1.PodRunning
		if iRunning != jRunning {
			return iRunning
		}
		return pods[i].CreationTimestamp.After(pods[j].CreationTimestamp.Time)
	})
	return pods[0]
}

// errNoPods reports that the Job/Deployment exists but has no pods (e.g. the Job
// was garbage-collected by its TTL, or the pods have not been created yet).
type errNoPods struct{ what string }

func (e errNoPods) Error() string { return "no pods found for " + e.what }

// writePodErr maps podFor* errors to HTTP responses.
func (b *Base) writePodErr(w http.ResponseWriter, r *http.Request, err error) {
	var np errNoPods
	if errors.As(err, &np) {
		writeError(w, http.StatusNotFound, np.Error())
		return
	}
	handleGetErr(w, r, err)
}

// podForJob returns the most recently created pod of a batch Job.
func (b *Base) podForJob(ctx context.Context, jobName string) (*corev1.Pod, error) {
	podList, err := b.KubeClient.CoreV1().Pods(b.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + jobName,
	})
	if err != nil {
		return nil, err
	}
	if len(podList.Items) == 0 {
		return nil, errNoPods{what: "step"}
	}
	sort.Slice(podList.Items, func(i, j int) bool {
		return podList.Items[i].CreationTimestamp.After(podList.Items[j].CreationTimestamp.Time)
	})
	return &podList.Items[0], nil
}

// podForDeployment returns the best pod of a Deployment and its first container's name.
func (b *Base) podForDeployment(ctx context.Context, deploymentName string) (*corev1.Pod, string, error) {
	deploy, err := b.KubeClient.AppsV1().Deployments(b.Namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return nil, "", err
	}
	selector, err := metav1.LabelSelectorAsSelector(deploy.Spec.Selector)
	if err != nil {
		return nil, "", err
	}
	podList, err := b.KubeClient.CoreV1().Pods(b.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return nil, "", err
	}
	if len(podList.Items) == 0 {
		return nil, "", errNoPods{what: "deployment"}
	}
	pod := pickBestPod(podList.Items)
	return &pod, deploy.Spec.Template.Spec.Containers[0].Name, nil
}

// tailLogs fetches the last MaxLogLines lines of one container as a snapshot,
// with a bounded timeout and a 4 MiB safety cap.
func (b *Base) tailLogs(ctx context.Context, podName, container string) ([]string, error) {
	tailLines := int64(b.MaxLogLines)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	stream, err := b.KubeClient.CoreV1().Pods(b.Namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: container,
		TailLines: &tailLines,
	}).Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	raw, err := io.ReadAll(io.LimitReader(stream, 4<<20))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = []string{}
	}
	return lines, nil
}

// publishSnapshot hands a snapshot to the log sink without blocking the response.
// context.Background() so the publish is not cancelled when the HTTP response returns.
func (b *Base) publishSnapshot(snap logsink.LogSnapshot) {
	go func() { _ = b.Sink.Publish(context.Background(), snap) }()
}
