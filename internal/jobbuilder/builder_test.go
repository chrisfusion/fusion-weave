// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package jobbuilder_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/jobbuilder"
	"fusion-platform.io/fusion-weave/internal/security"
)

func minJobTmpl() *weavev1alpha1.WeaveJobTemplate {
	return &weavev1alpha1.WeaveJobTemplate{
		Spec: weavev1alpha1.WeaveJobTemplateSpec{Image: "myrepo/myapp:1.0"},
	}
}

func minRun() *weavev1alpha1.WeaveRun {
	return &weavev1alpha1.WeaveRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run1", Namespace: "fusion"},
		Spec:       weavev1alpha1.WeaveRunSpec{ChainRef: corev1.LocalObjectReference{Name: "chain1"}},
	}
}

func TestBuild_AuthSecretName_SetsEnvFrom(t *testing.T) {
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(minJobTmpl(), step, minRun(), 0, "", "", security.Defaults{}, "my-auth-secret")
	c := job.Spec.Template.Spec.Containers[0]
	if len(c.EnvFrom) != 1 || c.EnvFrom[0].SecretRef == nil || c.EnvFrom[0].SecretRef.Name != "my-auth-secret" {
		t.Errorf("expected envFrom secretRef my-auth-secret, got %+v", c.EnvFrom)
	}
}

func TestBuild_EmptyAuthSecretName_NoEnvFrom(t *testing.T) {
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(minJobTmpl(), step, minRun(), 0, "", "", security.Defaults{}, "")
	c := job.Spec.Template.Spec.Containers[0]
	if len(c.EnvFrom) != 0 {
		t.Errorf("expected no envFrom when authSecretName is empty, got %+v", c.EnvFrom)
	}
}
