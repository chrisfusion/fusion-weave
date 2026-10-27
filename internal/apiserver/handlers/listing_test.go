// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

func listClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := weavev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

// get calls handler with GET target; name, if set, becomes the {name} URL parameter.
func get(t *testing.T, handler http.HandlerFunc, target, name string, out any) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if name != "" {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", name)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	}
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code == http.StatusOK && out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
	}
	return w.Code
}

func meta(name string, created time.Time, labels map[string]string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: "fusion", CreationTimestamp: metav1.NewTime(created), Labels: labels}
}

func mt(t time.Time) *metav1.Time { m := metav1.NewTime(t); return &m }

func itemNames[T any](items []T, name func(*T) string) []string {
	out := make([]string, len(items))
	for i := range items {
		out[i] = name(&items[i])
	}
	return out
}

var (
	base0    = time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	fluxLbl  = map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps"}
	wizLbl   = map[string]string{labelManagedBy: "wizard"}
	manLbl   = map[string]string{labelManagedBy: managedByManual}
	otherNs  = metav1.ObjectMeta{Name: "elsewhere", Namespace: "other"}
	appNames = func(l weavev1alpha1.WeaveAppList) []string {
		return itemNames(l.Items, func(a *weavev1alpha1.WeaveApp) string { return a.Name })
	}
)

func listApps() []client.Object {
	return []client.Object{
		&weavev1alpha1.WeaveApp{ObjectMeta: meta("web", base0, fluxLbl),
			Spec:   weavev1alpha1.WeaveAppSpec{Type: weavev1alpha1.WeaveAppTypeService, CodeSource: &weavev1alpha1.CodeSourceSpec{}},
			Status: weavev1alpha1.WeaveAppStatus{Phase: weavev1alpha1.WeaveAppPhaseReady}},
		&weavev1alpha1.WeaveApp{ObjectMeta: meta("etl-job", base0.Add(time.Hour), wizLbl),
			Spec: weavev1alpha1.WeaveAppSpec{Type: weavev1alpha1.WeaveAppTypeJob},
			Status: weavev1alpha1.WeaveAppStatus{Phase: weavev1alpha1.WeaveAppPhaseRunning, Executions: []weavev1alpha1.WeaveAppExecution{
				{Entrypoint: "load", JobName: "etl-job-load-1", Phase: weavev1alpha1.WeaveAppExecutionSucceeded, StartTime: mt(base0.Add(2 * time.Hour)), CompletionTime: mt(base0.Add(3 * time.Hour))},
				{Entrypoint: "clean", JobName: "etl-job-clean-1", Phase: weavev1alpha1.WeaveAppExecutionFailed, StartTime: mt(base0.Add(4 * time.Hour)), CompletionTime: mt(base0.Add(5 * time.Hour))},
				{Entrypoint: "load", JobName: "etl-job-load-2", Phase: weavev1alpha1.WeaveAppExecutionRunning, StartTime: mt(base0.Add(6 * time.Hour))},
			}}},
		&weavev1alpha1.WeaveApp{ObjectMeta: meta("report-job", base0.Add(2*time.Hour), manLbl),
			Spec: weavev1alpha1.WeaveAppSpec{Type: weavev1alpha1.WeaveAppTypeJob},
			Status: weavev1alpha1.WeaveAppStatus{Phase: weavev1alpha1.WeaveAppPhaseReady, Executions: []weavev1alpha1.WeaveAppExecution{
				{Entrypoint: "main", JobName: "report-job-main-1", Phase: weavev1alpha1.WeaveAppExecutionSucceeded, StartTime: mt(base0.Add(time.Hour))},
			}}},
		&weavev1alpha1.WeaveApp{ObjectMeta: otherNs},
	}
}

func TestAppList(t *testing.T) {
	h := NewAppHandler(listClient(t, listApps()...), "fusion", nil).(*AppHandler)
	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"etl-job", "report-job", "web"}},
		{"?type=job", []string{"etl-job", "report-job"}},
		{"?phase=Ready", []string{"report-job", "web"}},
		{"?phase=Running,Invalid", []string{"etl-job"}},
		{"?codeSource=true", []string{"web"}},
		{"?codeSource=false&running=false", []string{"report-job"}},
		{"?running=true", []string{"etl-job"}},
		{"?q=JOB", []string{"etl-job", "report-job"}},
		{"?source=gitops", []string{"web"}},
		{"?source=wizard&source=manual", []string{"etl-job", "report-job"}},
		{"?labelSelector=fusion-platform.io/managed-by%3Dwizard", []string{"etl-job"}},
		{"?sort=-created", []string{"report-job", "etl-job", "web"}},
		{"?sort=-lastExecution", []string{"etl-job", "report-job", "web"}},
		{"?since=" + base0.Add(30*time.Minute).Format(time.RFC3339), []string{"etl-job", "report-job"}},
	}
	for _, tc := range cases {
		var list weavev1alpha1.WeaveAppList
		if code := get(t, h.List, "/api/v1/apps"+tc.query, "", &list); code != http.StatusOK {
			t.Errorf("%s: status %d", tc.query, code)
			continue
		}
		if got := appNames(list); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.query, got, tc.want)
		}
	}
}

func TestAppListPagination(t *testing.T) {
	h := NewAppHandler(listClient(t, listApps()...), "fusion", nil).(*AppHandler)
	var page1, page2 weavev1alpha1.WeaveAppList
	get(t, h.List, "/api/v1/apps?limit=2", "", &page1)
	if !slices.Equal(appNames(page1), []string{"etl-job", "report-job"}) || page1.Continue == "" ||
		page1.RemainingItemCount == nil || *page1.RemainingItemCount != 1 {
		t.Fatalf("page 1: %v %+v", appNames(page1), page1.ListMeta)
	}
	get(t, h.List, "/api/v1/apps?limit=2&continue="+page1.Continue, "", &page2)
	if !slices.Equal(appNames(page2), []string{"web"}) || page2.Continue != "" || page2.RemainingItemCount != nil {
		t.Fatalf("page 2: %v %+v", appNames(page2), page2.ListMeta)
	}
}

func TestListBadParams(t *testing.T) {
	c := listClient(t)
	apps := NewAppHandler(c, "fusion", nil).(*AppHandler)
	runs := NewRunHandler(c, "fusion", nil).(*RunHandler)
	triggers := NewTriggerHandler(c, "fusion", nil).(*TriggerHandler)
	cases := []struct {
		handler http.HandlerFunc
		target  string
	}{
		{apps.List, "/api/v1/apps?type=daemon"},
		{apps.List, "/api/v1/apps?phase=Done"},
		{apps.List, "/api/v1/apps?running=maybe"},
		{apps.List, "/api/v1/apps?sort=size"},
		{apps.List, "/api/v1/apps?limit=0"},
		{apps.List, "/api/v1/apps?labelSelector=%3D%3D%3D"},
		{runs.List, "/api/v1/runs?phase=Done"},
		{runs.List, "/api/v1/runs?since=yesterday"},
		{runs.Facets, "/api/v1/runs/facets?phase=Done"},
		{runs.Facets, "/api/v1/runs/facets?source=a%20b"},
		{triggers.List, "/api/v1/triggers?type=Hourly"},
		{triggers.List, "/api/v1/triggers?quarantined=yes"},
	}
	for _, tc := range cases {
		if code := get(t, tc.handler, tc.target, "", nil); code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", tc.target, code)
		}
	}
	if code := get(t, apps.Executions, "/api/v1/apps/x/executions?phase=Done", "x", nil); code != http.StatusBadRequest {
		t.Errorf("executions bad phase: got %d", code)
	}
	if code := get(t, apps.Executions, "/api/v1/apps/x/executions?sort=bogus", "x", nil); code != http.StatusBadRequest {
		t.Errorf("executions bad sort: got %d", code)
	}
}

func TestAppExecutions(t *testing.T) {
	h := NewAppHandler(listClient(t, listApps()...), "fusion", nil).(*AppHandler)
	type execList struct {
		Metadata metav1.ListMeta                   `json:"metadata"`
		Items    []weavev1alpha1.WeaveAppExecution `json:"items"`
	}
	jobNames := func(l execList) []string {
		return itemNames(l.Items, func(e *weavev1alpha1.WeaveAppExecution) string { return e.JobName })
	}
	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"etl-job-load-2", "etl-job-clean-1", "etl-job-load-1"}},
		{"?entrypoint=load", []string{"etl-job-load-2", "etl-job-load-1"}},
		{"?phase=Failed,Running", []string{"etl-job-load-2", "etl-job-clean-1"}},
		{"?sort=started", []string{"etl-job-load-1", "etl-job-clean-1", "etl-job-load-2"}},
		{"?sort=entrypoint", []string{"etl-job-clean-1", "etl-job-load-1", "etl-job-load-2"}},
		{"?q=clean", []string{"etl-job-clean-1"}},
		{"?until=" + base0.Add(5*time.Hour).Format(time.RFC3339), []string{"etl-job-clean-1", "etl-job-load-1"}},
		{"?source=gitops", []string{}},
	}
	for _, tc := range cases {
		var list execList
		if code := get(t, h.Executions, "/api/v1/apps/etl-job/executions"+tc.query, "etl-job", &list); code != http.StatusOK {
			t.Errorf("%s: status %d", tc.query, code)
			continue
		}
		if got := jobNames(list); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.query, got, tc.want)
		}
	}

	var page execList
	get(t, h.Executions, "/api/v1/apps/etl-job/executions?limit=1", "etl-job", &page)
	if len(page.Items) != 1 || page.Metadata.Continue == "" || *page.Metadata.RemainingItemCount != 2 {
		t.Fatalf("paged executions: %+v", page)
	}

	if code := get(t, h.Executions, "/api/v1/apps/missing/executions", "missing", nil); code != http.StatusNotFound {
		t.Fatalf("missing app: got %d, want 404", code)
	}
}

func listRuns() []client.Object {
	run := func(name string, created time.Time, labels map[string]string, chain, trigger string,
		phase weavev1alpha1.WeaveRunPhase, start, end *metav1.Time) *weavev1alpha1.WeaveRun {
		r := &weavev1alpha1.WeaveRun{ObjectMeta: meta(name, created, labels),
			Spec:   weavev1alpha1.WeaveRunSpec{ChainRef: corev1.LocalObjectReference{Name: chain}},
			Status: weavev1alpha1.WeaveRunStatus{Phase: phase, StartTime: start, CompletionTime: end}}
		if trigger != "" {
			r.Spec.TriggerRef = &corev1.LocalObjectReference{Name: trigger}
		}
		return r
	}
	return []client.Object{
		run("run-a", base0, fluxLbl, "etl", "nightly", weavev1alpha1.RunPhaseSucceeded, mt(base0), mt(base0.Add(10*time.Minute))),
		run("run-b", base0.Add(time.Hour), manLbl, "etl", "", weavev1alpha1.RunPhaseFailed, mt(base0.Add(time.Hour)), mt(base0.Add(time.Hour+time.Minute))),
		run("run-c", base0.Add(2*time.Hour), wizLbl, "report", "hourly", weavev1alpha1.RunPhaseRunning, mt(base0.Add(2*time.Hour)), nil),
		run("run-d", base0.Add(3*time.Hour), nil, "report", "hourly", "", nil, nil),
		&weavev1alpha1.WeaveRun{ObjectMeta: otherNs},
	}
}

func TestRunList(t *testing.T) {
	h := NewRunHandler(listClient(t, listRuns()...), "fusion", nil).(*RunHandler)
	runNames := func(l weavev1alpha1.WeaveRunList) []string {
		return itemNames(l.Items, func(r *weavev1alpha1.WeaveRun) string { return r.Name })
	}
	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"run-d", "run-c", "run-b", "run-a"}},
		{"?phase=Pending", []string{"run-d"}},
		{"?phase=Succeeded,Failed", []string{"run-b", "run-a"}},
		{"?chain=report", []string{"run-d", "run-c"}},
		{"?trigger=nightly,hourly", []string{"run-d", "run-c", "run-a"}},
		{"?source=gitops", []string{"run-a"}},
		{"?sort=duration", []string{"run-d", "run-b", "run-a", "run-c"}},
		{"?sort=completed", []string{"run-c", "run-d", "run-a", "run-b"}},
		{"?sort=started", []string{"run-d", "run-a", "run-b", "run-c"}},
		{"?sort=name", []string{"run-a", "run-b", "run-c", "run-d"}},
		// run-d has not started: the window falls back to its creation time
		{"?since=" + base0.Add(90*time.Minute).Format(time.RFC3339), []string{"run-d", "run-c"}},
	}
	for _, tc := range cases {
		var list weavev1alpha1.WeaveRunList
		if code := get(t, h.List, "/api/v1/runs"+tc.query, "", &list); code != http.StatusOK {
			t.Errorf("%s: status %d", tc.query, code)
			continue
		}
		if got := runNames(list); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.query, got, tc.want)
		}
	}
}

func TestRunFacets(t *testing.T) {
	h := NewRunHandler(listClient(t, listRuns()...), "fusion", nil).(*RunHandler)
	var f runFacets
	if code := get(t, h.Facets, "/api/v1/runs/facets", "", &f); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if f.Total != 4 || f.Phase["Pending"] != 1 || f.Phase["Running"] != 1 || f.Chain["etl"] != 2 ||
		f.Trigger["hourly"] != 2 || f.Trigger[""] != 0 || len(f.Trigger) != 2 ||
		f.Source["gitops"] != 1 || f.Source["manual"] != 1 || f.Source["wizard"] != 1 {
		t.Fatalf("facets: %+v", f)
	}

	f = runFacets{}
	get(t, h.Facets, "/api/v1/runs/facets?chain=etl&limit=1", "", &f)
	if f.Total != 2 || f.Phase["Succeeded"] != 1 || f.Phase["Failed"] != 1 || len(f.Chain) != 1 {
		t.Fatalf("filtered facets ignore limit: %+v", f)
	}
}

func TestTriggerLists(t *testing.T) {
	trig := func(name string, created time.Time, typ weavev1alpha1.WeaveTriggerType, chain string, paused, active, quarantined bool, last *metav1.Time) client.Object {
		return &weavev1alpha1.WeaveTrigger{ObjectMeta: meta(name, created, nil),
			Spec:   weavev1alpha1.WeaveTriggerSpec{Type: typ, ChainRef: corev1.LocalObjectReference{Name: chain}, Paused: paused},
			Status: weavev1alpha1.WeaveTriggerStatus{Active: active, Quarantined: quarantined, LastScheduleTime: last}}
	}
	c := listClient(t,
		trig("cron-a", base0, weavev1alpha1.TriggerCron, "etl", false, true, false, mt(base0.Add(time.Hour))),
		trig("cron-b", base0.Add(time.Hour), weavev1alpha1.TriggerCron, "report", true, false, false, nil),
		trig("batch", base0.Add(2*time.Hour), weavev1alpha1.TriggerBatchCron, "etl", false, true, true, mt(base0.Add(3*time.Hour))),
		trig("kafka", base0.Add(3*time.Hour), weavev1alpha1.TriggerKafka, "etl", false, true, false, nil),
	)
	triggerNames := func(l weavev1alpha1.WeaveTriggerList) []string {
		return itemNames(l.Items, func(t *weavev1alpha1.WeaveTrigger) string { return t.Name })
	}
	all := NewTriggerHandler(c, "fusion", nil).(*TriggerHandler)
	cases := []struct {
		handler http.HandlerFunc
		query   string
		want    []string
	}{
		{all.List, "", []string{"batch", "cron-a", "cron-b", "kafka"}},
		{all.List, "?type=Cron", []string{"cron-a", "cron-b"}},
		{all.List, "?chain=etl&active=true", []string{"batch", "cron-a", "kafka"}},
		{all.List, "?paused=true", []string{"cron-b"}},
		{all.List, "?quarantined=true", []string{"batch"}},
		{all.List, "?sort=-lastSchedule", []string{"batch", "cron-a", "cron-b", "kafka"}},
		{all.List, "?sort=-created&limit=2", []string{"kafka", "batch"}},
		{NewBatchTriggerHandler(c, "fusion").List, "", []string{"batch"}},
		{NewBatchTriggerHandler(c, "fusion").List, "?type=Cron", []string{}},
		{NewKafkaTriggerHandler(c, "fusion").List, "?q=kaf", []string{"kafka"}},
	}
	for _, tc := range cases {
		var list weavev1alpha1.WeaveTriggerList
		if code := get(t, tc.handler, "/api/v1/triggers"+tc.query, "", &list); code != http.StatusOK {
			t.Errorf("%s: status %d", tc.query, code)
			continue
		}
		if got := triggerNames(list); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.query, got, tc.want)
		}
	}
}
