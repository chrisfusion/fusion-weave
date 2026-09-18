// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

func minWeaveRun(name string) *weavev1alpha1.WeaveRun {
	return &weavev1alpha1.WeaveRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fusion"},
		Spec:       weavev1alpha1.WeaveRunSpec{ChainRef: corev1.LocalObjectReference{Name: "chain1"}},
	}
}

func boolPtr(b bool) *bool { return &b }

// ---- resolveExternalAuthRef ----

func TestResolveExternalAuthRef_RunOverrideWins(t *testing.T) {
	r := &WeaveRunReconciler{
		Client:                      fake.NewClientBuilder().WithScheme(testScheme(t)).Build(),
		ExternalAuthServiceAccounts: []string{"run-sa", "chain-sa"},
	}
	run := minWeaveRun("run1")
	run.Spec.ExternalAuthRefOverride = &weavev1alpha1.WeaveExternalAuthRef{Mode: weavev1alpha1.ExternalAuthModeServiceAccount, Name: "run-sa"}
	chain := &weavev1alpha1.WeaveChain{
		Spec: weavev1alpha1.WeaveChainSpec{
			ExternalAuthRef: &weavev1alpha1.WeaveExternalAuthRef{Mode: weavev1alpha1.ExternalAuthModeServiceAccount, Name: "chain-sa"},
		},
	}
	ref, err := r.resolveExternalAuthRef(context.Background(), run, chain)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref == nil || ref.Name != "run-sa" {
		t.Errorf("expected run override (run-sa) to win, got %+v", ref)
	}
}

func TestResolveExternalAuthRef_TriggerOverrideWinsOverChain(t *testing.T) {
	trigger := &weavev1alpha1.WeaveTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "trig1", Namespace: "fusion"},
		Spec: weavev1alpha1.WeaveTriggerSpec{
			ExternalAuthRefOverride: &weavev1alpha1.WeaveExternalAuthRef{Mode: weavev1alpha1.ExternalAuthModeServiceAccount, Name: "trigger-sa"},
		},
	}
	r := &WeaveRunReconciler{
		Client:                      fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(trigger).Build(),
		ExternalAuthServiceAccounts: []string{"trigger-sa", "chain-sa"},
	}
	run := minWeaveRun("run1")
	run.Spec.TriggerRef = &corev1.LocalObjectReference{Name: "trig1"}
	chain := &weavev1alpha1.WeaveChain{
		Spec: weavev1alpha1.WeaveChainSpec{
			ExternalAuthRef: &weavev1alpha1.WeaveExternalAuthRef{Mode: weavev1alpha1.ExternalAuthModeServiceAccount, Name: "chain-sa"},
		},
	}
	ref, err := r.resolveExternalAuthRef(context.Background(), run, chain)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref == nil || ref.Name != "trigger-sa" {
		t.Errorf("expected trigger override (trigger-sa) to win over chain default, got %+v", ref)
	}
}

func TestResolveExternalAuthRef_ChainDefault_MissingTrigger(t *testing.T) {
	// A missing/deleted trigger falls back to the chain default rather than
	// failing the run, mirroring resolveAuthSecretName.
	r := &WeaveRunReconciler{
		Client:                      fake.NewClientBuilder().WithScheme(testScheme(t)).Build(),
		ExternalAuthServiceAccounts: []string{"chain-sa"},
	}
	run := minWeaveRun("run1")
	run.Spec.TriggerRef = &corev1.LocalObjectReference{Name: "does-not-exist"}
	chain := &weavev1alpha1.WeaveChain{
		Spec: weavev1alpha1.WeaveChainSpec{
			ExternalAuthRef: &weavev1alpha1.WeaveExternalAuthRef{Mode: weavev1alpha1.ExternalAuthModeServiceAccount, Name: "chain-sa"},
		},
	}
	ref, err := r.resolveExternalAuthRef(context.Background(), run, chain)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref == nil || ref.Name != "chain-sa" {
		t.Errorf("expected fallback to chain default (chain-sa), got %+v", ref)
	}
}

func TestResolveExternalAuthRef_NilWhenUnset(t *testing.T) {
	r := &WeaveRunReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).Build()}
	ref, err := r.resolveExternalAuthRef(context.Background(), minWeaveRun("run1"), &weavev1alpha1.WeaveChain{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref != nil {
		t.Errorf("expected nil ref when no level sets one, got %+v", ref)
	}
}

func TestResolveExternalAuthRef_RejectsNameNotInAllowlist(t *testing.T) {
	r := &WeaveRunReconciler{
		Client:                      fake.NewClientBuilder().WithScheme(testScheme(t)).Build(),
		ExternalAuthServiceAccounts: []string{"only-this-one"},
	}
	run := minWeaveRun("run1")
	run.Spec.ExternalAuthRefOverride = &weavev1alpha1.WeaveExternalAuthRef{Mode: weavev1alpha1.ExternalAuthModeServiceAccount, Name: "not-allowlisted"}
	_, err := r.resolveExternalAuthRef(context.Background(), run, &weavev1alpha1.WeaveChain{})
	if err == nil {
		t.Error("expected an error for a name not in the allowlist")
	}
}

func TestResolveExternalAuthRef_RejectsAtTriggerLevel(t *testing.T) {
	trigger := &weavev1alpha1.WeaveTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "trig1", Namespace: "fusion"},
		Spec: weavev1alpha1.WeaveTriggerSpec{
			ExternalAuthRefOverride: &weavev1alpha1.WeaveExternalAuthRef{Mode: weavev1alpha1.ExternalAuthModeOIDC, Name: "not-allowlisted"},
		},
	}
	r := &WeaveRunReconciler{
		Client:                  fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(trigger).Build(),
		ExternalAuthOIDCSecrets: []string{"only-this-secret"},
	}
	run := minWeaveRun("run1")
	run.Spec.TriggerRef = &corev1.LocalObjectReference{Name: "trig1"}
	_, err := r.resolveExternalAuthRef(context.Background(), run, &weavev1alpha1.WeaveChain{})
	if err == nil {
		t.Error("expected an error for a trigger-level override name not in the allowlist")
	}
}

// ---- resolveUnsafeEnvironmentInjector ----

func TestResolveUnsafeEnvironmentInjector_DefaultsTrue(t *testing.T) {
	r := &WeaveRunReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).Build()}
	got, err := r.resolveUnsafeEnvironmentInjector(context.Background(), minWeaveRun("run1"), &weavev1alpha1.WeaveChain{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected default true when no level sets the flag")
	}
}

func TestResolveUnsafeEnvironmentInjector_ChainFalse(t *testing.T) {
	r := &WeaveRunReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).Build()}
	chain := &weavev1alpha1.WeaveChain{Spec: weavev1alpha1.WeaveChainSpec{UnsafeEnvironmentInjector: boolPtr(false)}}
	got, err := r.resolveUnsafeEnvironmentInjector(context.Background(), minWeaveRun("run1"), chain)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected chain-level false to be honored")
	}
}

func TestResolveUnsafeEnvironmentInjector_RunOverridesChain(t *testing.T) {
	r := &WeaveRunReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).Build()}
	run := minWeaveRun("run1")
	run.Spec.UnsafeEnvironmentInjectorOverride = boolPtr(true)
	chain := &weavev1alpha1.WeaveChain{Spec: weavev1alpha1.WeaveChainSpec{UnsafeEnvironmentInjector: boolPtr(false)}}
	got, err := r.resolveUnsafeEnvironmentInjector(context.Background(), run, chain)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected run override (true) to win over chain default (false)")
	}
}

// ---- mintServiceAccountToken ----

func TestMintServiceAccountToken_SetsConfiguredAudience(t *testing.T) {
	var capturedAudiences []string
	var capturedExpiry *int64
	clientset := fakeclientset.NewSimpleClientset()
	clientset.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createAction, ok := action.(k8stesting.CreateActionImpl)
		if !ok {
			return false, nil, nil
		}
		if createAction.GetSubresource() != "token" {
			return false, nil, nil
		}
		tr, ok := createAction.GetObject().(*authenticationv1.TokenRequest)
		if !ok {
			return false, nil, nil
		}
		capturedAudiences = tr.Spec.Audiences
		capturedExpiry = tr.Spec.ExpirationSeconds
		return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{Token: "minted-token"}}, nil
	})

	r := &WeaveRunReconciler{KubeClient: clientset, ExternalAuthSAAudience: "fusion"}
	tok, err := r.mintServiceAccountToken(context.Background(), "fusion", "demo-sa", 15*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "minted-token" {
		t.Errorf("token: got %q, want minted-token", tok)
	}
	if len(capturedAudiences) != 1 || capturedAudiences[0] != "fusion" {
		t.Errorf("expected audience [fusion], got %v", capturedAudiences)
	}
	if capturedExpiry == nil || *capturedExpiry != 900 {
		t.Errorf("expected expirationSeconds=900, got %v", capturedExpiry)
	}
}

func TestMintServiceAccountToken_ClampsBelowKubernetesMinimum(t *testing.T) {
	// Kubernetes rejects TokenRequest.spec.expirationSeconds below 600 (10m) with
	// "may not specify a duration less than 10 minutes" — confirmed against a real
	// cluster. A short ActiveDeadlineSeconds-derived TTL must be clamped up before
	// calling CreateToken, or minting fails outright for short-lived jobs.
	var capturedExpiry *int64
	clientset := fakeclientset.NewSimpleClientset()
	clientset.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createAction, ok := action.(k8stesting.CreateActionImpl)
		if !ok || createAction.GetSubresource() != "token" {
			return false, nil, nil
		}
		tr := createAction.GetObject().(*authenticationv1.TokenRequest)
		capturedExpiry = tr.Spec.ExpirationSeconds
		return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{Token: "tok"}}, nil
	})

	r := &WeaveRunReconciler{KubeClient: clientset}
	if _, err := r.mintServiceAccountToken(context.Background(), "fusion", "demo-sa", 2*time.Minute); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedExpiry == nil || *capturedExpiry != 600 {
		t.Errorf("expected expirationSeconds clamped to 600 (10m), got %v", capturedExpiry)
	}
}

func TestMintServiceAccountToken_EmptyAudience_NoAudiencesSet(t *testing.T) {
	var capturedAudiences []string
	captured := false
	clientset := fakeclientset.NewSimpleClientset()
	clientset.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createAction, ok := action.(k8stesting.CreateActionImpl)
		if !ok || createAction.GetSubresource() != "token" {
			return false, nil, nil
		}
		tr := createAction.GetObject().(*authenticationv1.TokenRequest)
		capturedAudiences = tr.Spec.Audiences
		captured = true
		return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{Token: "tok"}}, nil
	})

	r := &WeaveRunReconciler{KubeClient: clientset} // ExternalAuthSAAudience left empty
	if _, err := r.mintServiceAccountToken(context.Background(), "fusion", "demo-sa", time.Minute); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !captured {
		t.Fatal("reactor was not invoked")
	}
	if capturedAudiences != nil {
		t.Errorf("expected no audiences set when ExternalAuthSAAudience is empty, got %v", capturedAudiences)
	}
}

// ---- deleteExternalAuthSecret ----

func TestDeleteExternalAuthSecret_NotFoundIsNotAnError(t *testing.T) {
	r := &WeaveRunReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).Build()}
	// Must not panic or log a fatal error path for a secret that was never created.
	r.deleteExternalAuthSecret(context.Background(), "fusion", "job-that-never-had-one")
}

func TestDeleteExternalAuthSecret_DeletesExistingSecret(t *testing.T) {
	secretName := "run1-step1-0-ext-auth"
	existing := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "fusion"}}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(existing).Build()
	r := &WeaveRunReconciler{Client: c}
	r.deleteExternalAuthSecret(context.Background(), "fusion", "run1-step1-0")

	var check corev1.Secret
	err := c.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: secretName}, &check)
	if !errors.IsNotFound(err) {
		t.Errorf("expected secret to be deleted, got err=%v", err)
	}
}
