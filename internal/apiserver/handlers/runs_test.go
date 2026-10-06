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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

func newRunHandlerForTest(t *testing.T, prefixes []string) *RunHandler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := weavev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return NewRunHandler(fake.NewClientBuilder().WithScheme(scheme).Build(), "fusion", prefixes).(*RunHandler)
}

func postRun(t *testing.T, h *RunHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)
	return w
}

func TestRunCreate_ImageOverrides(t *testing.T) {
	prefixes := []string{"reg.io/cust/"}
	tests := []struct {
		name      string
		prefixes  []string
		overrides string
		want      int
	}{
		{"valid", prefixes, `[{"stepName":"s","image":"reg.io/cust/app:1.0"}]`, http.StatusCreated},
		{"latest", prefixes, `[{"stepName":"s","image":"reg.io/cust/app:latest"}]`, http.StatusBadRequest},
		{"foreign registry", prefixes, `[{"stepName":"s","image":"evil.io/app:1.0"}]`, http.StatusBadRequest},
		{"duplicate step", prefixes, `[{"stepName":"s","image":"reg.io/cust/a:1"},{"stepName":"s","image":"reg.io/cust/a:2"}]`, http.StatusBadRequest},
		{"disabled", nil, `[{"stepName":"s","image":"reg.io/cust/app:1.0"}]`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newRunHandlerForTest(t, tt.prefixes)
			w := postRun(t, h, `{"metadata":{"name":"r1"},"spec":{"chainRef":{"name":"c"},"imageOverrides":`+tt.overrides+`}}`)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tt.want, w.Body.String())
			}
		})
	}
}

func TestRunCreate_NoImageOverrides_NeedsNoPrefixes(t *testing.T) {
	h := newRunHandlerForTest(t, nil)
	if w := postRun(t, h, `{"metadata":{"name":"r1"},"spec":{"chainRef":{"name":"c"}}}`); w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
}

func TestRunSetImage(t *testing.T) {
	h := newRunHandlerForTest(t, []string{"reg.io/cust/"})
	if w := postRun(t, h, `{"metadata":{"name":"r1"},"spec":{"chainRef":{"name":"c"},"imageOverrides":[{"stepName":"a","image":"reg.io/cust/a:1"}]}}`); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	do := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/r1/image", strings.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", "r1")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		h.SetImage(w, req)
		return w
	}
	if w := do(`{"stepName":"a","image":"reg.io/cust/a:2"}`); w.Code != http.StatusOK {
		t.Fatalf("update existing: %d %s", w.Code, w.Body.String())
	}
	if w := do(`{"stepName":"b","image":"reg.io/cust/b:1"}`); w.Code != http.StatusOK {
		t.Fatalf("add new: %d %s", w.Code, w.Body.String())
	}
	var got weavev1alpha1.WeaveRun
	if err := h.client.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: "r1"}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.ImageOverrides) != 2 || got.Spec.ImageOverrides[0].Image != "reg.io/cust/a:2" {
		t.Fatalf("unexpected overrides: %+v", got.Spec.ImageOverrides)
	}
	for _, bad := range []string{`{"stepName":"a","image":"reg.io/cust/a:latest"}`, `{"stepName":"a","image":"evil.io/a:1"}`, `{"image":"reg.io/cust/a:1"}`} {
		if w := do(bad); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: got %d", bad, w.Code)
		}
	}
}
