// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSplitAllowlist(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", []string{}},
		{"single", "sa-one", []string{"sa-one"}},
		{"multiple", "sa-one:sa-two:sa-three", []string{"sa-one", "sa-two", "sa-three"}},
		{"whitespace and empty entries trimmed", " sa-one : :sa-two:", []string{"sa-one", "sa-two"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitAllowlist(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("splitAllowlist(%q) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestExternalAuthOptionsHandler(t *testing.T) {
	handler := NewExternalAuthOptionsHandler("sa-one:sa-two", "oidc-secret")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/external-auth/options", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body ExternalAuthOptions
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	want := ExternalAuthOptions{
		ServiceAccounts: []string{"sa-one", "sa-two"},
		OIDCSecrets:     []string{"oidc-secret"},
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %#v, want %#v", body, want)
	}
}

func TestExternalAuthOptionsHandlerUnconfigured(t *testing.T) {
	handler := NewExternalAuthOptionsHandler("", "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/external-auth/options", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if got := rec.Body.String(); got != `{"serviceAccounts":[],"oidcSecrets":[]}`+"\n" {
		t.Errorf("body = %q, want empty-array JSON (not null)", got)
	}
}
