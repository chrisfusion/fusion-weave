// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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

func minValidJobTemplate() *weavev1alpha1.WeaveJobTemplate {
	return &weavev1alpha1.WeaveJobTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "tmpl1", Namespace: "fusion"},
		Spec:       weavev1alpha1.WeaveJobTemplateSpec{Image: "myrepo/myapp:1.0"},
		Status:     weavev1alpha1.WeaveJobTemplateStatus{Valid: true},
	}
}

func minChain() *weavev1alpha1.WeaveChain {
	return &weavev1alpha1.WeaveChain{
		ObjectMeta: metav1.ObjectMeta{Namespace: "fusion"},
		Spec: weavev1alpha1.WeaveChainSpec{
			Steps: []weavev1alpha1.WeaveChainStep{
				{Name: "step1", JobTemplateRef: &corev1.LocalObjectReference{Name: "tmpl1"}},
			},
		},
	}
}

func TestValidateChain_ExternalAuthRef_AllowedServiceAccount(t *testing.T) {
	r := &WeaveChainReconciler{
		Client:                      fake.NewClientBuilder().WithScheme(testScheme(t)).Build(),
		ExternalAuthServiceAccounts: []string{"demo-sa"},
	}
	chain := &weavev1alpha1.WeaveChain{
		Spec: weavev1alpha1.WeaveChainSpec{
			ExternalAuthRef: &weavev1alpha1.WeaveExternalAuthRef{
				Mode: weavev1alpha1.ExternalAuthModeServiceAccount,
				Name: "demo-sa",
			},
		},
	}
	valid, msg := r.validateChain(context.Background(), chain)
	if !valid {
		t.Errorf("expected valid, got invalid: %s", msg)
	}
}

func TestValidateChain_ExternalAuthRef_DisallowedServiceAccount(t *testing.T) {
	r := &WeaveChainReconciler{
		Client:                      fake.NewClientBuilder().WithScheme(testScheme(t)).Build(),
		ExternalAuthServiceAccounts: []string{"demo-sa"},
	}
	chain := &weavev1alpha1.WeaveChain{
		Spec: weavev1alpha1.WeaveChainSpec{
			ExternalAuthRef: &weavev1alpha1.WeaveExternalAuthRef{
				Mode: weavev1alpha1.ExternalAuthModeServiceAccount,
				Name: "not-allowlisted",
			},
		},
	}
	valid, _ := r.validateChain(context.Background(), chain)
	if valid {
		t.Error("expected invalid for a ServiceAccount name not in the allowlist")
	}
}

func TestValidateChain_ExternalAuthRef_AllowedOIDCSecret(t *testing.T) {
	r := &WeaveChainReconciler{
		Client:                  fake.NewClientBuilder().WithScheme(testScheme(t)).Build(),
		ExternalAuthOIDCSecrets: []string{"demo-oidc"},
	}
	chain := &weavev1alpha1.WeaveChain{
		Spec: weavev1alpha1.WeaveChainSpec{
			ExternalAuthRef: &weavev1alpha1.WeaveExternalAuthRef{
				Mode: weavev1alpha1.ExternalAuthModeOIDC,
				Name: "demo-oidc",
			},
		},
	}
	valid, msg := r.validateChain(context.Background(), chain)
	if !valid {
		t.Errorf("expected valid, got invalid: %s", msg)
	}
}

func TestValidateChain_ExternalAuthRef_InvalidMode(t *testing.T) {
	// Constructed directly in Go, bypassing the CRD's +kubebuilder:validation:Enum
	// marker (which the fake client does not enforce) — exercises the Go-level
	// defense-in-depth default: branch.
	r := &WeaveChainReconciler{
		Client: fake.NewClientBuilder().WithScheme(testScheme(t)).Build(),
	}
	chain := &weavev1alpha1.WeaveChain{
		Spec: weavev1alpha1.WeaveChainSpec{
			ExternalAuthRef: &weavev1alpha1.WeaveExternalAuthRef{
				Mode: "bogus",
				Name: "whatever",
			},
		},
	}
	valid, msg := r.validateChain(context.Background(), chain)
	if valid {
		t.Error("expected invalid for an out-of-enum mode")
	}
	if msg == "" {
		t.Error("expected a validation message explaining the invalid mode")
	}
}

func TestValidateChain_NoExternalAuthRef_Valid(t *testing.T) {
	r := &WeaveChainReconciler{
		Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(minValidJobTemplate()).Build(),
	}
	valid, msg := r.validateChain(context.Background(), minChain())
	if !valid {
		t.Errorf("expected valid when externalAuthRef is unset, got invalid: %s", msg)
	}
}
