// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
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
