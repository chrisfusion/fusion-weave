// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/trigger"
)

// TestCreateRuns_CopyTriggerImageOverrides covers every trigger type: the runs
// built by createRun (cron/webhook/on-demand), createBatchRun and createKafkaRun
// must all carry the trigger's spec.imageOverrides.
func TestCreateRuns_CopyTriggerImageOverrides(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := weavev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	want := []weavev1alpha1.WeaveRunImageOverride{
		{StepName: "job", Image: "reg.io/cust/app:1.0", ImagePullPolicy: corev1.PullIfNotPresent},
	}
	newTrigger := func() *weavev1alpha1.WeaveTrigger {
		return &weavev1alpha1.WeaveTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: "ft", Namespace: "fusion"},
			Spec: weavev1alpha1.WeaveTriggerSpec{
				ChainRef:       corev1.LocalObjectReference{Name: "chain1"},
				ImageOverrides: want,
			},
		}
	}
	fires := map[string]func(r *WeaveTriggerReconciler, ft *weavev1alpha1.WeaveTrigger) error{
		"cron/webhook": func(r *WeaveTriggerReconciler, ft *weavev1alpha1.WeaveTrigger) error {
			return r.createRun(context.Background(), ft, nil)
		},
		"batchcron": func(r *WeaveTriggerReconciler, ft *weavev1alpha1.WeaveTrigger) error {
			return r.createBatchRun(context.Background(), ft, trigger.BatchFireRequest{JobID: "j1"})
		},
		"kafka": func(r *WeaveTriggerReconciler, ft *weavev1alpha1.WeaveTrigger) error {
			return r.createKafkaRun(context.Background(), ft, trigger.KafkaFireRequest{})
		},
	}
	for name, fire := range fires {
		t.Run(name, func(t *testing.T) {
			ft := newTrigger()
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ft).WithStatusSubresource(ft).Build()
			r := &WeaveTriggerReconciler{Client: c, Scheme: scheme}
			if err := fire(r, ft); err != nil {
				t.Fatal(err)
			}
			var runs weavev1alpha1.WeaveRunList
			if err := c.List(context.Background(), &runs, client.InNamespace("fusion")); err != nil {
				t.Fatal(err)
			}
			if len(runs.Items) != 1 {
				t.Fatalf("got %d runs, want 1", len(runs.Items))
			}
			if got := runs.Items[0].Spec.ImageOverrides; !reflect.DeepEqual(got, want) {
				t.Errorf("run imageOverrides = %v, want %v", got, want)
			}
		})
	}
}
