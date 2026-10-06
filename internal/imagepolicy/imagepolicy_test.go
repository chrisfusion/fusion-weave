// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package imagepolicy_test

import (
	"reflect"
	"strings"
	"testing"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/imagepolicy"
)

func TestParsePrefixes(t *testing.T) {
	got := imagepolicy.ParsePrefixes(" reg.io:5000/a/ , ,reg.io/b/,")
	want := []string{"reg.io:5000/a/", "reg.io/b/"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := imagepolicy.ParsePrefixes(""); len(got) != 0 {
		t.Errorf("empty input should yield no prefixes, got %v", got)
	}
}

func TestValidate(t *testing.T) {
	prefixes := []string{"reg.io/cust-a/", "localhost:5000/"}
	tests := []struct {
		name     string
		image    string
		prefixes []string
		wantErr  string
	}{
		{"tagged and allowed", "reg.io/cust-a/app:1.2.3", prefixes, ""},
		{"digest and allowed", "reg.io/cust-a/app@sha256:abcd", prefixes, ""},
		{"registry port with tag", "localhost:5000/app:v1", prefixes, ""},
		{"latest rejected", "reg.io/cust-a/app:latest", prefixes, "explicit tag"},
		{"untagged rejected", "reg.io/cust-a/app", prefixes, "explicit tag"},
		{"untagged with registry port rejected", "localhost:5000/app", prefixes, "explicit tag"},
		{"foreign prefix rejected", "evil.io/app:1.0", prefixes, "allowed prefix"},
		{"disabled without prefixes", "reg.io/cust-a/app:1.0", nil, "disabled"},
		{"whitespace rejected", "reg.io/cust-a/app:1.0 --x", prefixes, "whitespace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := imagepolicy.Validate(tt.image, tt.prefixes)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateOverrides(t *testing.T) {
	kinds := map[string]weavev1alpha1.WeaveStepKind{
		"job":    weavev1alpha1.StepKindJob,
		"svc":    weavev1alpha1.StepKindDeploy,
		"shared": weavev1alpha1.StepKindDeploy,
	}
	owned := map[string]bool{"svc": true}
	prefixes := []string{"reg.io/"}
	ov := func(step, img string) weavev1alpha1.WeaveRunImageOverride {
		return weavev1alpha1.WeaveRunImageOverride{StepName: step, Image: img}
	}
	tests := []struct {
		name    string
		in      []weavev1alpha1.WeaveRunImageOverride
		wantErr string
	}{
		{"job and run-owned deploy ok", []weavev1alpha1.WeaveRunImageOverride{ov("job", "reg.io/a:1"), ov("svc", "reg.io/b:1")}, ""},
		{"unknown step", []weavev1alpha1.WeaveRunImageOverride{ov("nope", "reg.io/a:1")}, "unknown step"},
		{"duplicate", []weavev1alpha1.WeaveRunImageOverride{ov("job", "reg.io/a:1"), ov("job", "reg.io/a:2")}, "duplicate"},
		{"chain-owned deploy rejected", []weavev1alpha1.WeaveRunImageOverride{ov("shared", "reg.io/a:1")}, "run-owned"},
		{"bad image", []weavev1alpha1.WeaveRunImageOverride{ov("job", "reg.io/a:latest")}, "explicit tag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := imagepolicy.ValidateOverrides(tt.in, kinds, owned, prefixes)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}
