// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := weavev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add weave scheme: %v", err)
	}
	return scheme
}

func TestStripObjectMeta(t *testing.T) {
	meta := metav1.ObjectMeta{
		Name:              "my-chain",
		Namespace:         "fusion",
		Labels:            map[string]string{"team": "data"},
		Annotations:       map[string]string{"note": "keep me"},
		ResourceVersion:   "12345",
		UID:               "abc-123",
		Generation:        7,
		CreationTimestamp: metav1.NewTime(time.Now()),
		ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "kubectl"}},
		SelfLink:          "/apis/weave.fusion-platform.io/v1alpha1/namespaces/fusion/weavechains/my-chain",
	}

	stripObjectMeta(&meta)

	if meta.Name != "my-chain" || meta.Namespace != "fusion" {
		t.Errorf("Name/Namespace should survive, got Name=%q Namespace=%q", meta.Name, meta.Namespace)
	}
	if meta.Labels["team"] != "data" {
		t.Errorf("Labels should survive, got %v", meta.Labels)
	}
	if meta.Annotations["note"] != "keep me" {
		t.Errorf("Annotations should survive, got %v", meta.Annotations)
	}
	if meta.ResourceVersion != "" || meta.UID != "" || meta.Generation != 0 ||
		!meta.CreationTimestamp.IsZero() || meta.ManagedFields != nil || meta.SelfLink != "" {
		t.Errorf("server-managed fields should be zeroed, got %+v", meta)
	}
}

func seededClient(t *testing.T) *fake.ClientBuilder {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(testScheme(t))
}

func TestDumpObjects_SetsTypeMetaAndStripsStatus(t *testing.T) {
	jt := &weavev1alpha1.WeaveJobTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "build", Namespace: "fusion"},
		Status:     weavev1alpha1.WeaveJobTemplateStatus{Valid: true, ValidationMessage: "ok"},
	}
	chain := &weavev1alpha1.WeaveChain{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "fusion"},
		Status:     weavev1alpha1.WeaveChainStatus{Valid: true},
	}
	c := seededClient(t).WithObjects(jt, chain).Build()

	var buf strings.Builder
	count, err := DumpObjects(context.Background(), c, "fusion", &buf)
	if err != nil {
		t.Fatalf("DumpObjects: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 objects, got %d", count)
	}

	docs := strings.Split(buf.String(), "---\n")
	if len(docs) != 2 {
		t.Fatalf("expected 2 yaml documents, got %d:\n%s", len(docs), buf.String())
	}

	for _, doc := range docs {
		var generic map[string]interface{}
		if err := yaml.Unmarshal([]byte(doc), &generic); err != nil {
			t.Fatalf("unmarshal document: %v\n%s", err, doc)
		}
		if generic["apiVersion"] != "weave.fusion-platform.io/v1alpha1" {
			t.Errorf("expected apiVersion set, got %v in doc:\n%s", generic["apiVersion"], doc)
		}
		if generic["kind"] == nil || generic["kind"] == "" {
			t.Errorf("expected kind set, got %v in doc:\n%s", generic["kind"], doc)
		}
		// Status must be reset to its zero value, not carry over the seeded
		// Valid: true from the source objects above.
		if status, ok := generic["status"].(map[string]interface{}); ok {
			if v, ok := status["valid"]; ok && v != false {
				t.Errorf("expected status.valid reset to false, got %v in doc:\n%s", v, doc)
			}
			if v, ok := status["validationMessage"]; ok && v != "" {
				t.Errorf("expected status.validationMessage reset to empty, got %v in doc:\n%s", v, doc)
			}
		}
	}
}

func TestDumpObjects_Ordering(t *testing.T) {
	// Seed in reverse dependency order to prove DumpObjects imposes its own order
	// (templates, chain, trigger) rather than trusting List's return order.
	trigger := &weavev1alpha1.WeaveTrigger{ObjectMeta: metav1.ObjectMeta{Name: "t1", Namespace: "fusion"}}
	chain := &weavev1alpha1.WeaveChain{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "fusion"}}
	svcTmpl := &weavev1alpha1.WeaveServiceTemplate{ObjectMeta: metav1.ObjectMeta{Name: "s1", Namespace: "fusion"}}
	jobTmpl := &weavev1alpha1.WeaveJobTemplate{ObjectMeta: metav1.ObjectMeta{Name: "j1", Namespace: "fusion"}}

	c := seededClient(t).WithObjects(trigger, chain, svcTmpl, jobTmpl).Build()

	var buf strings.Builder
	if _, err := DumpObjects(context.Background(), c, "fusion", &buf); err != nil {
		t.Fatalf("DumpObjects: %v", err)
	}

	kindsInOrder := []string{"WeaveJobTemplate", "WeaveServiceTemplate", "WeaveChain", "WeaveTrigger"}
	out := buf.String()
	lastIdx := -1
	for _, kind := range kindsInOrder {
		idx := strings.Index(out, "kind: "+kind)
		if idx == -1 {
			t.Fatalf("expected %s in output:\n%s", kind, out)
		}
		if idx <= lastIdx {
			t.Fatalf("expected %s to appear after previous kind, order violated:\n%s", kind, out)
		}
		lastIdx = idx
	}
}

func TestDumpObjects_ExcludesOtherNamespace(t *testing.T) {
	inNS := &weavev1alpha1.WeaveChain{ObjectMeta: metav1.ObjectMeta{Name: "in", Namespace: "fusion"}}
	outNS := &weavev1alpha1.WeaveChain{ObjectMeta: metav1.ObjectMeta{Name: "out", Namespace: "other"}}
	c := seededClient(t).WithObjects(inNS, outNS).Build()

	var buf strings.Builder
	count, err := DumpObjects(context.Background(), c, "fusion", &buf)
	if err != nil {
		t.Fatalf("DumpObjects: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 object scoped to namespace, got %d", count)
	}
}
