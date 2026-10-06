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

func TestTriggerCreate_ImageOverrides(t *testing.T) {
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
			scheme := runtime.NewScheme()
			if err := clientgoscheme.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := weavev1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			h := NewTriggerHandler(fake.NewClientBuilder().WithScheme(scheme).Build(), "fusion", tt.prefixes)
			body := `{"metadata":{"name":"t1"},"spec":{"type":"OnDemand","chainRef":{"name":"c"},"imageOverrides":` + tt.overrides + `}}`
			w := httptest.NewRecorder()
			h.Create(w, httptest.NewRequest(http.MethodPost, "/api/v1/triggers", strings.NewReader(body)))
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tt.want, w.Body.String())
			}
		})
	}
}
