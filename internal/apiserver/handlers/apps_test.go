// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

func postApp(t *testing.T, prefixes []string, body string) *httptest.ResponseRecorder {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := weavev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	h := NewAppHandler(fake.NewClientBuilder().WithScheme(scheme).Build(), "fusion", prefixes)
	w := httptest.NewRecorder()
	h.Create(w, httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader(body)))
	return w
}

func TestAppCreate_ImagePolicy(t *testing.T) {
	prefixes := []string{"reg.io/team/"}
	tests := []struct {
		name     string
		prefixes []string
		spec     string
		want     int
	}{
		{"no policy configured", nil, `{"type":"job","image":"anything:latest"}`, http.StatusCreated},
		{"allowed image", prefixes, `{"type":"job","image":"reg.io/team/app:1.0"}`, http.StatusCreated},
		{"foreign image", prefixes, `{"type":"job","image":"evil.io/app:1.0"}`, http.StatusBadRequest},
		{"latest image", prefixes, `{"type":"job","image":"reg.io/team/app:latest"}`, http.StatusBadRequest},
		{"foreign loader image", prefixes, `{"type":"job","image":"reg.io/team/app:1.0","codeSource":{"artifactName":"a","tag":"t","loaderImage":"evil.io/l:1"}}`, http.StatusBadRequest},
		{"allowed loader image", prefixes, `{"type":"job","image":"reg.io/team/app:1.0","codeSource":{"artifactName":"a","tag":"t","loaderImage":"reg.io/team/l:1"}}`, http.StatusCreated},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := postApp(t, tc.prefixes, `{"metadata":{"name":"x"},"spec":`+tc.spec+`}`)
			if w.Code != tc.want {
				t.Fatalf("got %d (%s), want %d", w.Code, w.Body.String(), tc.want)
			}
		})
	}
}

func TestAppFire(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := weavev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	app := func(name string, typ weavev1alpha1.WeaveAppType, running string) *weavev1alpha1.WeaveApp {
		a := &weavev1alpha1.WeaveApp{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fusion"},
			Spec: weavev1alpha1.WeaveAppSpec{Type: typ, Image: "busybox:1.36",
				Entrypoints: []weavev1alpha1.WeaveAppEntrypoint{{Name: "main"}, {Name: "nightly", Schedule: "0 0 3 * * *"}}},
		}
		if running != "" {
			a.Status.Executions = []weavev1alpha1.WeaveAppExecution{{Entrypoint: running, Phase: weavev1alpha1.WeaveAppExecutionRunning}}
		}
		return a
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&weavev1alpha1.WeaveApp{}).
		WithObjects(app("job", weavev1alpha1.WeaveAppTypeJob, "nightly"), app("svc", weavev1alpha1.WeaveAppTypeService, "")).Build()
	h := NewAppHandler(c, "fusion", nil).(*AppHandler)

	fire := func(name, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/"+name+"/fire", strings.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", name)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		h.Fire(w, req)
		return w
	}
	tests := []struct {
		name, app, body string
		want            int
	}{
		{"missing entrypoint", "job", `{}`, http.StatusBadRequest},
		{"unknown entrypoint", "job", `{"entrypoint":"nope"}`, http.StatusBadRequest},
		{"service app", "svc", `{"entrypoint":"main"}`, http.StatusBadRequest},
		{"still running", "job", `{"entrypoint":"nightly"}`, http.StatusConflict},
		{"unknown app", "gone", `{"entrypoint":"main"}`, http.StatusNotFound},
		{"fires", "job", `{"entrypoint":"main"}`, http.StatusAccepted},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if w := fire(tc.app, tc.body); w.Code != tc.want {
				t.Fatalf("got %d (%s), want %d", w.Code, w.Body.String(), tc.want)
			}
		})
	}

	var got weavev1alpha1.WeaveApp
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: "job"}, &got); err != nil {
		t.Fatal(err)
	}
	if ep := got.Annotations[weavev1alpha1.WeaveAppFireAnnotation]; ep != "main" {
		t.Fatalf("fire annotation = %q, want main", ep)
	}
}
