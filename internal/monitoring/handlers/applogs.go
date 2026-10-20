// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/monitoring/logsink"
)

// AppLogResponse is the JSON body returned by the WeaveApp logs endpoint.
type AppLogResponse struct {
	AppName string `json:"appName"`
	Type    string `json:"type"`
	// Entrypoint and Execution (the Job name) are set for type=job apps only.
	Entrypoint string   `json:"entrypoint,omitempty"`
	Execution  string   `json:"execution,omitempty"`
	PodName    string   `json:"podName"`
	Container  string   `json:"container"`
	Lines      []string `json:"lines"`
}

// AppLogsHandler serves GET /monitor/v1/apps/{name}/logs.
type AppLogsHandler struct{ Base }

func NewAppLogsHandler(b Base) *AppLogsHandler { return &AppLogsHandler{b} }

// Get returns a snapshot of the last N log lines of a WeaveApp's pod.
//
//   - type=service: the best pod of the app's Deployment.
//   - type=job: the pod of the newest execution, narrowed by ?entrypoint=<name>
//     or pinned to one run with ?execution=<jobName> (from status.executions).
//
// ?container=code-loader returns the init container's log instead of the main
// container's (bad INDEX_URL, missing artifact, ...). A finished execution whose
// Job expired (executionTTLSeconds) has no pod any more and yields 404; its
// outcome stays in status.executions.
func (h *AppLogsHandler) Get(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	entrypoint := r.URL.Query().Get("entrypoint")
	execution := r.URL.Query().Get("execution")
	initContainer := false
	switch c := r.URL.Query().Get("container"); c {
	case "", "main":
	case "code-loader":
		initContainer = true
	default:
		writeError(w, http.StatusBadRequest, `container must be "main" or "code-loader"`)
		return
	}

	key := "app:logs:" + name + ":" + entrypoint + ":" + execution + ":" + r.URL.Query().Get("container")
	if h.cacheGet(w, key) {
		return
	}

	var app weavev1alpha1.WeaveApp
	if err := h.Client.Get(r.Context(), client.ObjectKey{Namespace: h.Namespace, Name: name}, &app); err != nil {
		handleGetErr(w, r, err)
		return
	}

	resp := AppLogResponse{AppName: name, Type: string(app.Spec.Type)}
	var (
		pod       *corev1.Pod
		container string
		err       error
	)
	switch app.Spec.Type {
	case weavev1alpha1.WeaveAppTypeService:
		if app.Status.DeploymentName == "" {
			writeError(w, http.StatusNotFound, "app has no deployment yet")
			return
		}
		pod, container, err = h.podForDeployment(r.Context(), app.Status.DeploymentName)
	case weavev1alpha1.WeaveAppTypeJob:
		exec := pickExecution(&app, entrypoint, execution)
		if exec == nil {
			writeError(w, http.StatusNotFound, "no matching execution")
			return
		}
		resp.Entrypoint, resp.Execution = exec.Entrypoint, exec.JobName
		pod, err = h.podForJob(r.Context(), exec.JobName)
		container = "job"
	default:
		writeError(w, http.StatusNotFound, "app has an unknown type")
		return
	}
	if err != nil {
		h.writePodErr(w, r, err)
		return
	}
	if initContainer {
		container = "code-loader"
	}

	lines, err := h.tailLogs(r.Context(), pod.Name, container)
	if err != nil {
		internalError(w, r, err)
		return
	}
	resp.PodName, resp.Container, resp.Lines = pod.Name, container, lines
	h.Cache.Set(key, resp)

	stepName := "app"
	if resp.Entrypoint != "" {
		stepName = resp.Entrypoint
	}
	h.publishSnapshot(logsink.LogSnapshot{
		RunName:   name,
		StepName:  stepName,
		PodName:   pod.Name,
		Lines:     lines,
		FetchedAt: time.Now(),
	})
	writeJSON(w, http.StatusOK, resp)
}

// pickExecution returns the execution named by jobName, else the newest one
// (optionally of one entrypoint). status.executions is newest first.
func pickExecution(app *weavev1alpha1.WeaveApp, entrypoint, jobName string) *weavev1alpha1.WeaveAppExecution {
	for i := range app.Status.Executions {
		e := &app.Status.Executions[i]
		if jobName != "" && e.JobName != jobName {
			continue
		}
		if entrypoint != "" && e.Entrypoint != entrypoint {
			continue
		}
		return e
	}
	return nil
}
