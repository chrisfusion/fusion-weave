// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package backup

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

func TestHasExistingObjects(t *testing.T) {
	empty := seededClient(t).Build()
	has, err := HasExistingObjects(context.Background(), empty, "fusion")
	if err != nil {
		t.Fatalf("HasExistingObjects: %v", err)
	}
	if has {
		t.Error("expected false for empty namespace")
	}

	trigger := &weavev1alpha1.WeaveTrigger{ObjectMeta: metav1.ObjectMeta{Name: "t1", Namespace: "fusion"}}
	withTrigger := seededClient(t).WithObjects(trigger).Build()
	has, err = HasExistingObjects(context.Background(), withTrigger, "fusion")
	if err != nil {
		t.Fatalf("HasExistingObjects: %v", err)
	}
	if !has {
		t.Error("expected true when a WeaveTrigger exists")
	}
}

func TestDumpRestoreRoundTrip(t *testing.T) {
	jt := &weavev1alpha1.WeaveJobTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "build", Namespace: "fusion"},
		Spec:       weavev1alpha1.WeaveJobTemplateSpec{Image: "alpine:3.19"},
		Status:     weavev1alpha1.WeaveJobTemplateStatus{Valid: true},
	}
	chain := &weavev1alpha1.WeaveChain{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "fusion"},
		Status:     weavev1alpha1.WeaveChainStatus{Valid: true},
	}
	src := seededClient(t).WithObjects(jt, chain).Build()

	var buf strings.Builder
	count, err := DumpObjects(context.Background(), src, "fusion", &buf)
	if err != nil {
		t.Fatalf("DumpObjects: %v", err)
	}

	dst := seededClient(t).Build()
	result, err := RestoreObjects(context.Background(), dst, strings.NewReader(buf.String()), "fusion")
	if err != nil {
		t.Fatalf("RestoreObjects: %v", err)
	}
	if result.Created != count {
		t.Fatalf("expected %d created, got %d (errors: %v)", count, result.Created, result.Errors)
	}
	if result.Skipped != 0 || len(result.Errors) != 0 {
		t.Fatalf("expected no skips/errors, got skipped=%d errors=%v", result.Skipped, result.Errors)
	}

	var restoredChain weavev1alpha1.WeaveChain
	if err := dst.Get(context.Background(), client.ObjectKey{Name: "demo", Namespace: "fusion"}, &restoredChain); err != nil {
		t.Fatalf("get restored chain: %v", err)
	}
	if restoredChain.Status.Valid {
		t.Error("restored object should have a fresh (empty) status, not the source's")
	}
}

func TestRestoreObjects_AlreadyExistsIsSkippedNotFatal(t *testing.T) {
	existing := &weavev1alpha1.WeaveChain{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "fusion"}}
	dst := seededClient(t).WithObjects(existing).Build()

	stream := "apiVersion: weave.fusion-platform.io/v1alpha1\n" +
		"kind: WeaveChain\n" +
		"metadata:\n  name: demo\n  namespace: fusion\n" +
		"---\n" +
		"apiVersion: weave.fusion-platform.io/v1alpha1\n" +
		"kind: WeaveTrigger\n" +
		"metadata:\n  name: t1\n  namespace: fusion\n"

	result, err := RestoreObjects(context.Background(), dst, strings.NewReader(stream), "fusion")
	if err != nil {
		t.Fatalf("RestoreObjects: %v", err)
	}
	if result.Skipped != 1 {
		t.Errorf("expected 1 skipped (AlreadyExists), got %d", result.Skipped)
	}
	if result.Created != 1 {
		t.Errorf("expected 1 created (the trigger), got %d", result.Created)
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected no fatal errors, got %v", result.Errors)
	}
}

func TestRestoreObjects_ForcesTargetNamespace(t *testing.T) {
	dst := seededClient(t).Build()
	stream := "apiVersion: weave.fusion-platform.io/v1alpha1\n" +
		"kind: WeaveChain\n" +
		"metadata:\n  name: demo\n  namespace: some-other-namespace\n"

	if _, err := RestoreObjects(context.Background(), dst, strings.NewReader(stream), "fusion"); err != nil {
		t.Fatalf("RestoreObjects: %v", err)
	}

	var restored weavev1alpha1.WeaveChain
	if err := dst.Get(context.Background(), client.ObjectKey{Name: "demo", Namespace: "fusion"}, &restored); err != nil {
		t.Fatalf("expected object created in target namespace fusion: %v", err)
	}
}

func TestRestoreObjects_UnrecognizedKindErrors(t *testing.T) {
	dst := seededClient(t).Build()
	stream := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: not-ours\n"
	if _, err := RestoreObjects(context.Background(), dst, strings.NewReader(stream), "fusion"); err == nil {
		t.Fatal("expected an error for an unrecognized kind, got nil")
	}
}

func TestRestoreObjects_SkipsBlankDocuments(t *testing.T) {
	dst := seededClient(t).Build()
	stream := "---\n\n---\n" +
		"apiVersion: weave.fusion-platform.io/v1alpha1\nkind: WeaveTrigger\nmetadata:\n  name: t1\n  namespace: fusion\n" +
		"---\n"

	result, err := RestoreObjects(context.Background(), dst, strings.NewReader(stream), "fusion")
	if err != nil {
		t.Fatalf("RestoreObjects: %v", err)
	}
	if result.Created != 1 {
		t.Fatalf("expected 1 created, got %d (errors: %v)", result.Created, result.Errors)
	}
}
