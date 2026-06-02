// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package indexclient

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestParseMetadata_Full(t *testing.T) {
	data := []byte(`
maintainer: "alice@example.com"
runner:
  type: python
  port: 8080
  builderImage: builder:1.0
  args:
    LOG_LEVEL: debug
    WORKERS: "4"
ingress:
  pathPrefix: myapp
resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 512Mi
`)
	meta, err := parseMetadata(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Maintainer != "alice@example.com" {
		t.Errorf("maintainer: got %q, want alice@example.com", meta.Maintainer)
	}
	if meta.Runner.Type != "python" {
		t.Errorf("runner.type: got %q, want python", meta.Runner.Type)
	}
	if meta.Runner.Port != 8080 {
		t.Errorf("runner.port: got %d, want 8080", meta.Runner.Port)
	}
	if meta.Runner.BuilderImage != "builder:1.0" {
		t.Errorf("runner.builderImage: got %q, want builder:1.0", meta.Runner.BuilderImage)
	}
	if meta.Runner.Args["LOG_LEVEL"] != "debug" {
		t.Errorf("runner.args[LOG_LEVEL]: got %q, want debug", meta.Runner.Args["LOG_LEVEL"])
	}
	if meta.Runner.Args["WORKERS"] != "4" {
		t.Errorf("runner.args[WORKERS]: got %q, want 4", meta.Runner.Args["WORKERS"])
	}
	if meta.Ingress.PathPrefix != "myapp" {
		t.Errorf("ingress.pathPrefix: got %q, want myapp", meta.Ingress.PathPrefix)
	}
	wantCPUReq := resource.MustParse("100m")
	if got := meta.Resources.Requests[corev1.ResourceCPU]; got.Cmp(wantCPUReq) != 0 {
		t.Errorf("requests.cpu: got %v, want %v", got, wantCPUReq)
	}
	wantMemLimit := resource.MustParse("512Mi")
	if got := meta.Resources.Limits[corev1.ResourceMemory]; got.Cmp(wantMemLimit) != 0 {
		t.Errorf("limits.memory: got %v, want %v", got, wantMemLimit)
	}
}

func TestParseMetadata_Empty(t *testing.T) {
	meta, err := parseMetadata([]byte("{}"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Runner.Type != "" || meta.Runner.Port != 0 || meta.Runner.BuilderImage != "" {
		t.Errorf("expected zero runner, got %+v", meta.Runner)
	}
	if meta.Ingress.PathPrefix != "" {
		t.Errorf("expected empty ingress pathPrefix, got %q", meta.Ingress.PathPrefix)
	}
	if meta.Maintainer != "" {
		t.Errorf("expected empty maintainer, got %q", meta.Maintainer)
	}
	if len(meta.Resources.Requests) > 0 || len(meta.Resources.Limits) > 0 {
		t.Errorf("expected zero resources, got %+v", meta.Resources)
	}
}

func TestParseMetadata_MissingRunner(t *testing.T) {
	// Only maintainer present — runner section entirely absent.
	meta, err := parseMetadata([]byte(`maintainer: "bob"`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Runner.Port != 0 {
		t.Errorf("runner.port should be 0 when runner key absent, got %d", meta.Runner.Port)
	}
	if meta.Runner.Type != "" {
		t.Errorf("runner.type should be empty when runner key absent, got %q", meta.Runner.Type)
	}
	if meta.Runner.BuilderImage != "" {
		t.Errorf("runner.builderImage should be empty when runner key absent, got %q", meta.Runner.BuilderImage)
	}
}

func TestParseMetadata_RunnerPortZero(t *testing.T) {
	// runner.port absent → zero value; callers use `Port > 0` to gate on it.
	meta, err := parseMetadata([]byte("runner:\n  type: node\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Runner.Port != 0 {
		t.Errorf("runner.port should be 0 when not set, got %d", meta.Runner.Port)
	}
	if meta.Runner.Type != "node" {
		t.Errorf("runner.type: got %q, want node", meta.Runner.Type)
	}
}

func TestParseMetadata_MissingBuilderImage(t *testing.T) {
	meta, err := parseMetadata([]byte("runner:\n  type: python\n  port: 5000\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Runner.BuilderImage != "" {
		t.Errorf("runner.builderImage should be empty when absent, got %q", meta.Runner.BuilderImage)
	}
}

func TestParseMetadata_MissingIngress(t *testing.T) {
	meta, err := parseMetadata([]byte("runner:\n  port: 8080\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Ingress.PathPrefix != "" {
		t.Errorf("ingress.pathPrefix should be empty when ingress section absent, got %q", meta.Ingress.PathPrefix)
	}
}

func TestParseMetadata_InvalidYAML(t *testing.T) {
	_, err := parseMetadata([]byte("runner: [not: closed"))
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestParseMetadata_InvalidResourceQuantity_SilentlySkipped(t *testing.T) {
	// Bad quantities are silently dropped; valid ones survive.
	data := []byte(`
resources:
  requests:
    cpu: "not-a-quantity"
    memory: 128Mi
  limits:
    cpu: 500m
    memory: "also-bad"
`)
	meta, err := parseMetadata(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := meta.Resources.Requests[corev1.ResourceCPU]; ok {
		t.Error("invalid cpu request should have been skipped, but it was kept")
	}
	wantMem := resource.MustParse("128Mi")
	if got := meta.Resources.Requests[corev1.ResourceMemory]; got.Cmp(wantMem) != 0 {
		t.Errorf("memory request: got %v, want %v", got, wantMem)
	}
	wantCPU := resource.MustParse("500m")
	if got := meta.Resources.Limits[corev1.ResourceCPU]; got.Cmp(wantCPU) != 0 {
		t.Errorf("cpu limit: got %v, want %v", got, wantCPU)
	}
	if _, ok := meta.Resources.Limits[corev1.ResourceMemory]; ok {
		t.Error("invalid memory limit should have been skipped, but it was kept")
	}
}

func TestParseMetadata_OnlyRequests(t *testing.T) {
	// Only requests set — Resources should still be populated (not zero).
	data := []byte("resources:\n  requests:\n    cpu: 250m\n")
	meta, err := parseMetadata(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(meta.Resources.Requests) == 0 {
		t.Error("expected non-empty Requests when only requests are set")
	}
	if len(meta.Resources.Limits) != 0 {
		t.Errorf("expected empty Limits when only requests are set, got %v", meta.Resources.Limits)
	}
}

func TestParseMetadata_RunnerArgsAbsent(t *testing.T) {
	// No runner.args key → Args is nil/empty; ranging over it must be a no-op.
	data := []byte("runner:\n  type: python\n  port: 5000\n")
	meta, err := parseMetadata(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(meta.Runner.Args) != 0 {
		t.Errorf("expected empty runner.args when absent, got %v", meta.Runner.Args)
	}
}

func TestParseMetadata_RunnerArgs(t *testing.T) {
	data := []byte(`
runner:
  args:
    LOG_LEVEL: info
    MAX_RETRIES: "3"
`)
	meta, err := parseMetadata(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(meta.Runner.Args) != 2 {
		t.Fatalf("expected 2 runner.args, got %d: %v", len(meta.Runner.Args), meta.Runner.Args)
	}
	if meta.Runner.Args["LOG_LEVEL"] != "info" {
		t.Errorf("runner.args[LOG_LEVEL]: got %q, want info", meta.Runner.Args["LOG_LEVEL"])
	}
	if meta.Runner.Args["MAX_RETRIES"] != "3" {
		t.Errorf("runner.args[MAX_RETRIES]: got %q, want 3", meta.Runner.Args["MAX_RETRIES"])
	}
}

func TestParseMetadata_PortAsQuotedString_ReturnsError(t *testing.T) {
	// sigs.k8s.io/yaml converts YAML to JSON before unmarshalling. A quoted port
	// string "8080" becomes a JSON string, which encoding/json cannot unmarshal
	// into int32 — this must return an error, not silently produce Port==0.
	_, err := parseMetadata([]byte("runner:\n  port: \"8080\"\n"))
	if err == nil {
		t.Fatal("expected error for quoted port string (YAML string cannot unmarshal into int32), got nil")
	}
}

func TestParseMetadata_UnknownFieldsIgnored(t *testing.T) {
	// Extra fields not in rawMetadata are silently dropped; known fields still parse.
	data := []byte(`
runner:
  type: go
  port: 3000
  unknownField: whatever
  nested:
    also: ignored
maintainer: "dev@example.com"
futureTopLevelKey: somevalue
`)
	meta, err := parseMetadata(data)
	if err != nil {
		t.Fatalf("unexpected error for unknown YAML fields: %v", err)
	}
	if meta.Runner.Type != "go" {
		t.Errorf("runner.type: got %q, want go", meta.Runner.Type)
	}
	if meta.Runner.Port != 3000 {
		t.Errorf("runner.port: got %d, want 3000", meta.Runner.Port)
	}
	if meta.Maintainer != "dev@example.com" {
		t.Errorf("maintainer: got %q, want dev@example.com", meta.Maintainer)
	}
}
