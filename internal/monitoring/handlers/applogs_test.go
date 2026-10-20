// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/monitoring/cache"
	"fusion-platform.io/fusion-weave/internal/monitoring/logsink"
)

func appLogsHandler(t *testing.T, app *weavev1alpha1.WeaveApp, kubeObjs ...runtime.Object) *AppLogsHandler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := weavev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app).Build()
	b := NewBase(c, fakeclientset.NewSimpleClientset(kubeObjs...), "fusion",
		cache.New[string, any](time.Second), logsink.NoopSink{}, 100)
	return NewAppLogsHandler(b)
}

func getAppLogs(t *testing.T, h *AppLogsHandler, name, query string) (int, AppLogResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/monitor/v1/apps/"+name+"/logs"+query, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", name)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	var resp AppLogResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func jobPod(name, job string, created time.Time) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "fusion",
		Labels:            map[string]string{"job-name": job},
		CreationTimestamp: metav1.NewTime(created),
	}}
}

func jobAppWithExecutions() *weavev1alpha1.WeaveApp {
	return &weavev1alpha1.WeaveApp{
		ObjectMeta: metav1.ObjectMeta{Name: "etl", Namespace: "fusion"},
		Spec:       weavev1alpha1.WeaveAppSpec{Type: weavev1alpha1.WeaveAppTypeJob},
		Status: weavev1alpha1.WeaveAppStatus{Executions: []weavev1alpha1.WeaveAppExecution{ // newest first
			{Entrypoint: "adhoc", JobName: "etl-3-adhoc-0"},
			{Entrypoint: "nightly", JobName: "etl-2-nightly-0"},
			{Entrypoint: "adhoc", JobName: "etl-1-adhoc-0"},
		}},
	}
}

func TestAppLogs_JobDefaultsToNewestExecution(t *testing.T) {
	now := time.Now()
	h := appLogsHandler(t, jobAppWithExecutions(),
		jobPod("pod-new", "etl-3-adhoc-0", now),
		jobPod("pod-old", "etl-1-adhoc-0", now.Add(-time.Hour)))
	code, resp := getAppLogs(t, h, "etl", "")
	if code != http.StatusOK || resp.PodName != "pod-new" || resp.Execution != "etl-3-adhoc-0" || resp.Container != "job" {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
	if len(resp.Lines) == 0 {
		t.Error("expected log lines")
	}
}

func TestAppLogs_JobEntrypointAndExecutionSelectors(t *testing.T) {
	now := time.Now()
	h := appLogsHandler(t, jobAppWithExecutions(),
		jobPod("pod-new", "etl-3-adhoc-0", now),
		jobPod("pod-nightly", "etl-2-nightly-0", now.Add(-time.Minute)),
		jobPod("pod-old", "etl-1-adhoc-0", now.Add(-time.Hour)))

	if _, resp := getAppLogs(t, h, "etl", "?entrypoint=nightly"); resp.PodName != "pod-nightly" {
		t.Errorf("entrypoint selector: %+v", resp)
	}
	if _, resp := getAppLogs(t, h, "etl", "?execution=etl-1-adhoc-0"); resp.PodName != "pod-old" {
		t.Errorf("execution selector: %+v", resp)
	}
	if code, _ := getAppLogs(t, h, "etl", "?execution=ghost"); code != http.StatusNotFound {
		t.Errorf("unknown execution: got %d, want 404", code)
	}
	if code, _ := getAppLogs(t, h, "etl", "?entrypoint=ghost"); code != http.StatusNotFound {
		t.Errorf("unknown entrypoint: got %d, want 404", code)
	}
}

func TestAppLogs_JobExpiredWithoutPodIs404(t *testing.T) {
	h := appLogsHandler(t, jobAppWithExecutions()) // Jobs/pods already garbage-collected
	if code, _ := getAppLogs(t, h, "etl", ""); code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", code)
	}
}

func TestAppLogs_CodeLoaderContainer(t *testing.T) {
	h := appLogsHandler(t, jobAppWithExecutions(), jobPod("pod-new", "etl-3-adhoc-0", time.Now()))
	if _, resp := getAppLogs(t, h, "etl", "?container=code-loader"); resp.Container != "code-loader" {
		t.Errorf("got %+v", resp)
	}
	if code, _ := getAppLogs(t, h, "etl", "?container=evil"); code != http.StatusBadRequest {
		t.Errorf("invalid container: got %d, want 400", code)
	}
}

func TestAppLogs_ServicePicksRunningPodOfDeployment(t *testing.T) {
	labels := map[string]string{"app": "site"}
	app := &weavev1alpha1.WeaveApp{
		ObjectMeta: metav1.ObjectMeta{Name: "site", Namespace: "fusion"},
		Spec:       weavev1alpha1.WeaveAppSpec{Type: weavev1alpha1.WeaveAppTypeService},
		Status:     weavev1alpha1.WeaveAppStatus{DeploymentName: "site-app"},
	}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "site-app", Namespace: "fusion"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "service"}}}},
		},
	}
	pending := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "fusion", Labels: labels,
		CreationTimestamp: metav1.NewTime(time.Now())}, Status: corev1.PodStatus{Phase: corev1.PodPending}}
	running := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "running", Namespace: "fusion", Labels: labels,
		CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}

	h := appLogsHandler(t, app, deploy, pending, running)
	code, resp := getAppLogs(t, h, "site", "")
	if code != http.StatusOK || resp.PodName != "running" || resp.Container != "service" || resp.Type != "service" {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
}

func TestAppLogs_ServiceWithoutDeploymentIs404(t *testing.T) {
	app := &weavev1alpha1.WeaveApp{
		ObjectMeta: metav1.ObjectMeta{Name: "site", Namespace: "fusion"},
		Spec:       weavev1alpha1.WeaveAppSpec{Type: weavev1alpha1.WeaveAppTypeService},
	}
	if code, _ := getAppLogs(t, appLogsHandler(t, app), "site", ""); code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", code)
	}
}

func TestAppLogs_UnknownApp404(t *testing.T) {
	if code, _ := getAppLogs(t, appLogsHandler(t, jobAppWithExecutions()), "nope", ""); code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", code)
	}
}
