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
