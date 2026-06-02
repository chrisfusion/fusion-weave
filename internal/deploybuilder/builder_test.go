// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package deploybuilder_test

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/deploybuilder"
	"fusion-platform.io/fusion-weave/internal/indexclient"
	"fusion-platform.io/fusion-weave/internal/security"
)

// minTmpl returns the smallest valid WeaveServiceTemplate for use in tests.
func minTmpl(image string) *weavev1alpha1.WeaveServiceTemplate {
	return &weavev1alpha1.WeaveServiceTemplate{
		Spec: weavev1alpha1.WeaveServiceTemplateSpec{
			Image: image,
			Ports: []weavev1alpha1.WeaveServicePort{
				{Name: "http", Port: 9000, Protocol: corev1.ProtocolTCP},
			},
		},
	}
}

// minOverride returns a minimal WeaveRunStepOverride for use in tests.
func minOverride(artifact, tag string) *weavev1alpha1.WeaveRunStepOverride {
	return &weavev1alpha1.WeaveRunStepOverride{
		StepName:     "step1",
		ArtifactName: artifact,
		Tag:          tag,
	}
}

// envVar looks up an env var by name in a slice.
func envVar(env []corev1.EnvVar, name string) (string, bool) {
	for _, e := range env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// ---- Build (chain-owned Deployment) ----

func TestBuild_NoCodeSource_NoInitContainers(t *testing.T) {
	tmpl := minTmpl("myrepo/myapp:1.0")
	deploy := deploybuilder.Build(tmpl, "chain", "step", "fusion", security.Defaults{}, nil, "", "", "", nil)
	if len(deploy.Spec.Template.Spec.InitContainers) != 0 {
		t.Errorf("expected no init containers without codeSource, got %d", len(deploy.Spec.Template.Spec.InitContainers))
	}
	c := deploy.Spec.Template.Spec.Containers[0]
	if _, ok := envVar(c.Env, "WEAVE_ARTIFACT"); ok {
		t.Error("WEAVE_ARTIFACT should not be set without codeSource")
	}
}

func TestBuild_NilMeta_WithCodeSource_BaseVarsOnly(t *testing.T) {
	// meta=nil simulates a best-effort fetch failure.
	// Base WEAVE_* vars (ARTIFACT, TAG, VERSION, NAMESPACE, MOUNT_PATH) are always set.
	// Optional vars (PORT, RUNNER_TYPE, etc.) must be absent.
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{
		ArtifactName: "org.myteam.myapp",
		Tag:          "stable",
	}
	deploy := deploybuilder.Build(tmpl, "chain", "step", "fusion", security.Defaults{}, nil, "1.0.0", "", "", nil)
	c := deploy.Spec.Template.Spec.Containers[0]

	if v, ok := envVar(c.Env, "WEAVE_ARTIFACT"); !ok || v != "org.myteam.myapp" {
		t.Errorf("WEAVE_ARTIFACT: got %q (found=%v)", v, ok)
	}
	if v, ok := envVar(c.Env, "WEAVE_VERSION"); !ok || v != "1.0.0" {
		t.Errorf("WEAVE_VERSION: got %q (found=%v)", v, ok)
	}
	if _, ok := envVar(c.Env, "WEAVE_PORT"); ok {
		t.Error("WEAVE_PORT must not be set when meta is nil")
	}
	if _, ok := envVar(c.Env, "WEAVE_RUNNER_TYPE"); ok {
		t.Error("WEAVE_RUNNER_TYPE must not be set when meta is nil")
	}
	if _, ok := envVar(c.Env, "WEAVE_BUILDER_IMAGE"); ok {
		t.Error("WEAVE_BUILDER_IMAGE must not be set when meta is nil")
	}
}

func TestBuild_MetaPort_SetsEnvVar_DoesNotOverrideContainerPorts(t *testing.T) {
	// Key asymmetry: in Build (chain-owned), meta.Runner.Port only sets WEAVE_PORT
	// as an env var. It does NOT replace the container or service ports from the
	// template. Callers that need port override must use BuildFromOverride instead.
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{
		ArtifactName: "org.myteam.myapp",
		Tag:          "stable",
	}
	meta := &indexclient.AppMetadata{
		Runner: indexclient.AppRunner{Port: 8080},
	}
	deploy := deploybuilder.Build(tmpl, "chain", "step", "fusion", security.Defaults{}, meta, "1.0.0", "", "", nil)
	c := deploy.Spec.Template.Spec.Containers[0]

	if v, ok := envVar(c.Env, "WEAVE_PORT"); !ok || v != "8080" {
		t.Errorf("WEAVE_PORT: got %q (found=%v)", v, ok)
	}
	// Template port (9000) must still be on the container — not replaced by meta port.
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != 9000 {
		t.Errorf("container port should remain 9000 from template, got %+v", c.Ports)
	}
}

func TestBuild_EmptyImage_PassesThrough(t *testing.T) {
	// Builder does not validate the image; empty string is passed to the container
	// spec as-is. Kubelet will reject the pod, but the builder does not error.
	tmpl := minTmpl("")
	deploy := deploybuilder.Build(tmpl, "chain", "step", "fusion", security.Defaults{}, nil, "", "", "", nil)
	if c := deploy.Spec.Template.Spec.Containers[0]; c.Image != "" {
		t.Errorf("expected empty image to pass through, got %q", c.Image)
	}
}

func TestBuild_NoCommand_ContainerCommandIsNil(t *testing.T) {
	// No Command in template is valid — container uses its own entrypoint.
	// Builder must not inject a default command.
	tmpl := minTmpl("myrepo/myapp:1.0")
	deploy := deploybuilder.Build(tmpl, "chain", "step", "fusion", security.Defaults{}, nil, "", "", "", nil)
	if c := deploy.Spec.Template.Spec.Containers[0]; len(c.Command) != 0 {
		t.Errorf("expected nil command when not set in template, got %v", c.Command)
	}
}

func TestBuild_DefaultReplicas(t *testing.T) {
	// Replicas==0 (zero value) must default to 1.
	tmpl := minTmpl("myrepo/myapp:1.0")
	deploy := deploybuilder.Build(tmpl, "chain", "step", "fusion", security.Defaults{}, nil, "", "", "", nil)
	if *deploy.Spec.Replicas != 1 {
		t.Errorf("expected default replicas=1, got %d", *deploy.Spec.Replicas)
	}
}

func TestBuild_CodeSource_DefaultMountPath(t *testing.T) {
	// Empty MountPath in CodeSourceSpec must fall back to "/weave-code".
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{
		ArtifactName: "org.myteam.myapp",
		Tag:          "stable",
		// MountPath deliberately empty
	}
	deploy := deploybuilder.Build(tmpl, "chain", "step", "fusion", security.Defaults{}, nil, "", "", "", nil)
	inits := deploy.Spec.Template.Spec.InitContainers
	if len(inits) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(inits))
	}
	if v, ok := envVar(inits[0].Env, "MOUNT_PATH"); !ok || v != "/weave-code" {
		t.Errorf("MOUNT_PATH: got %q (found=%v), want /weave-code", v, ok)
	}
}

func TestBuild_MetaRunnerType_SetsEnvVar(t *testing.T) {
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.app", Tag: "stable"}
	meta := &indexclient.AppMetadata{Runner: indexclient.AppRunner{Type: "python"}}
	deploy := deploybuilder.Build(tmpl, "chain", "step", "fusion", security.Defaults{}, meta, "", "", "", nil)
	c := deploy.Spec.Template.Spec.Containers[0]
	if v, ok := envVar(c.Env, "WEAVE_RUNNER_TYPE"); !ok || v != "python" {
		t.Errorf("WEAVE_RUNNER_TYPE: got %q (found=%v)", v, ok)
	}
}

func TestBuild_EmptyRunnerType_EnvVarAbsent(t *testing.T) {
	// runner.type="" → WEAVE_RUNNER_TYPE must not be set (not an empty-string env var).
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.app", Tag: "stable"}
	meta := &indexclient.AppMetadata{Runner: indexclient.AppRunner{Type: ""}}
	deploy := deploybuilder.Build(tmpl, "chain", "step", "fusion", security.Defaults{}, meta, "", "", "", nil)
	c := deploy.Spec.Template.Spec.Containers[0]
	if _, ok := envVar(c.Env, "WEAVE_RUNNER_TYPE"); ok {
		t.Error("WEAVE_RUNNER_TYPE must not be set when runner.type is empty")
	}
}

// ---- BuildFromOverride (run-owned Deployment) ----

func TestBuildFromOverride_MetaPort_OverridesTemplatePorts(t *testing.T) {
	// Unlike Build, BuildFromOverride replaces container AND service ports with meta.Runner.Port.
	tmpl := minTmpl("myrepo/myapp:1.0") // template port is 9000
	meta := &indexclient.AppMetadata{
		Runner: indexclient.AppRunner{Port: 8080},
	}
	deploy := deploybuilder.BuildFromOverride(
		tmpl, minOverride("org.myapp", "stable"), meta,
		"run1", "step1", "fusion", security.Defaults{}, "1.0.0", "", "", nil,
	)
	c := deploy.Spec.Template.Spec.Containers[0]
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != 8080 {
		t.Errorf("expected container port 8080 from metadata, got %+v", c.Ports)
	}
	if v, ok := envVar(c.Env, "WEAVE_PORT"); !ok || v != "8080" {
		t.Errorf("WEAVE_PORT: got %q (found=%v)", v, ok)
	}
}

func TestBuildFromOverride_MetaPortZero_UsesTemplatePorts(t *testing.T) {
	// meta.Runner.Port==0 → template ports are used unchanged.
	tmpl := minTmpl("myrepo/myapp:1.0")
	meta := &indexclient.AppMetadata{Runner: indexclient.AppRunner{Port: 0}}
	deploy := deploybuilder.BuildFromOverride(
		tmpl, minOverride("org.myapp", "stable"), meta,
		"run1", "step1", "fusion", security.Defaults{}, "1.0.0", "", "", nil,
	)
	c := deploy.Spec.Template.Spec.Containers[0]
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != 9000 {
		t.Errorf("expected template port 9000 when meta.Port==0, got %+v", c.Ports)
	}
	if _, ok := envVar(c.Env, "WEAVE_PORT"); ok {
		t.Error("WEAVE_PORT must not be set when meta.Runner.Port==0")
	}
}

func TestBuildFromOverride_MetaResources_OverridesTemplate(t *testing.T) {
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("50m"),
		},
	}
	meta := &indexclient.AppMetadata{
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		},
	}
	deploy := deploybuilder.BuildFromOverride(
		tmpl, minOverride("org.myapp", "stable"), meta,
		"run1", "step1", "fusion", security.Defaults{}, "1.0.0", "", "", nil,
	)
	c := deploy.Spec.Template.Spec.Containers[0]
	want := resource.MustParse("200m")
	if got := c.Resources.Requests[corev1.ResourceCPU]; got.Cmp(want) != 0 {
		t.Errorf("cpu request: got %v, want %v (meta should win)", got, want)
	}
}

func TestBuildFromOverride_EmptyMetaResources_UsesTemplate(t *testing.T) {
	// meta.Resources has no requests or limits → template resources are used.
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("50m"),
		},
	}
	meta := &indexclient.AppMetadata{} // zero Resources
	deploy := deploybuilder.BuildFromOverride(
		tmpl, minOverride("org.myapp", "stable"), meta,
		"run1", "step1", "fusion", security.Defaults{}, "1.0.0", "", "", nil,
	)
	c := deploy.Spec.Template.Spec.Containers[0]
	want := resource.MustParse("50m")
	if got := c.Resources.Requests[corev1.ResourceCPU]; got.Cmp(want) != 0 {
		t.Errorf("cpu request: got %v, want %v (template should be used when meta.Resources empty)", got, want)
	}
}

func TestBuildFromOverride_NilMeta_UsesTemplate(t *testing.T) {
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("50m"),
		},
	}
	deploy := deploybuilder.BuildFromOverride(
		tmpl, minOverride("org.myapp", "stable"), nil,
		"run1", "step1", "fusion", security.Defaults{}, "1.0.0", "", "", nil,
	)
	c := deploy.Spec.Template.Spec.Containers[0]
	want := resource.MustParse("50m")
	if got := c.Resources.Requests[corev1.ResourceCPU]; got.Cmp(want) != 0 {
		t.Errorf("cpu request: got %v, want %v (template should be used when meta is nil)", got, want)
	}
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != 9000 {
		t.Errorf("expected template port 9000 when meta is nil, got %+v", c.Ports)
	}
}

func TestBuildFromOverride_EmptyImage_PassesThrough(t *testing.T) {
	// Empty image in the template is passed through without error.
	// The resulting pod will fail to start (kubelet rejects empty image), but
	// that is caught at runtime, not here.
	tmpl := minTmpl("")
	deploy := deploybuilder.BuildFromOverride(
		tmpl, minOverride("org.myapp", "stable"), nil,
		"run1", "step1", "fusion", security.Defaults{}, "1.0.0", "", "", nil,
	)
	if c := deploy.Spec.Template.Spec.Containers[0]; c.Image != "" {
		t.Errorf("expected empty image to pass through, got %q", c.Image)
	}
}

func TestBuildFromOverride_NoCommand_ContainerCommandIsNil(t *testing.T) {
	// No Command/Args in template → container uses its image entrypoint.
	tmpl := minTmpl("myrepo/myapp:1.0")
	deploy := deploybuilder.BuildFromOverride(
		tmpl, minOverride("org.myapp", "stable"), nil,
		"run1", "step1", "fusion", security.Defaults{}, "1.0.0", "", "", nil,
	)
	c := deploy.Spec.Template.Spec.Containers[0]
	if len(c.Command) != 0 {
		t.Errorf("expected nil command when not set in template, got %v", c.Command)
	}
	if len(c.Args) != 0 {
		t.Errorf("expected nil args when not set in template, got %v", c.Args)
	}
}

func TestBuildFromOverride_RunnerArgs_InjectedAsEnvVars(t *testing.T) {
	tmpl := minTmpl("myrepo/myapp:1.0")
	meta := &indexclient.AppMetadata{
		Runner: indexclient.AppRunner{
			Args: map[string]string{
				"LOG_LEVEL": "debug",
				"WORKERS":   "4",
			},
		},
	}
	deploy := deploybuilder.BuildFromOverride(
		tmpl, minOverride("org.myapp", "stable"), meta,
		"run1", "step1", "fusion", security.Defaults{}, "1.0.0", "", "", nil,
	)
	c := deploy.Spec.Template.Spec.Containers[0]
	if v, ok := envVar(c.Env, "LOG_LEVEL"); !ok || v != "debug" {
		t.Errorf("LOG_LEVEL: got %q (found=%v)", v, ok)
	}
	if v, ok := envVar(c.Env, "WORKERS"); !ok || v != "4" {
		t.Errorf("WORKERS: got %q (found=%v)", v, ok)
	}
}

func TestBuildFromOverride_NoRunnerArgs_NoExtraEnvVars(t *testing.T) {
	tmpl := minTmpl("myrepo/myapp:1.0")
	meta := &indexclient.AppMetadata{Runner: indexclient.AppRunner{Port: 8080}}
	deploy := deploybuilder.BuildFromOverride(
		tmpl, minOverride("org.myapp", "stable"), meta,
		"run1", "step1", "fusion", security.Defaults{}, "1.0.0", "", "", nil,
	)
	c := deploy.Spec.Template.Spec.Containers[0]
	// Env must only contain the standard WEAVE_* vars, no extras.
	known := map[string]bool{
		"WEAVE_ARTIFACT": true, "WEAVE_TAG": true, "WEAVE_VERSION": true,
		"WEAVE_NAMESPACE": true, "WEAVE_MOUNT_PATH": true, "WEAVE_PORT": true,
	}
	for _, e := range c.Env {
		if !known[e.Name] {
			t.Errorf("unexpected env var %q injected when runner.args is empty", e.Name)
		}
	}
}

// ---- BuildServiceFromOverride ----

func TestBuildServiceFromOverride_MetaPort_ReplacesPorts(t *testing.T) {
	tmpl := minTmpl("myrepo/myapp:1.0") // template port 9000
	meta := &indexclient.AppMetadata{Runner: indexclient.AppRunner{Port: 8080}}
	svc := deploybuilder.BuildServiceFromOverride(tmpl, meta, "run1", "step1", "fusion")
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 8080 {
		t.Errorf("expected service port 8080 from metadata, got %+v", svc.Spec.Ports)
	}
	if svc.Spec.Ports[0].Name != "http" {
		t.Errorf("expected port name 'http', got %q", svc.Spec.Ports[0].Name)
	}
}

func TestBuildServiceFromOverride_NilMeta_UsesTemplatePorts(t *testing.T) {
	tmpl := minTmpl("myrepo/myapp:1.0")
	svc := deploybuilder.BuildServiceFromOverride(tmpl, nil, "run1", "step1", "fusion")
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 9000 {
		t.Errorf("expected template port 9000 when meta is nil, got %+v", svc.Spec.Ports)
	}
}

func TestBuildServiceFromOverride_NoTemplatePorts_NoMetaPort_EmptyServicePorts(t *testing.T) {
	// Neither template ports nor meta port → Service has no ports.
	// Kubernetes allows this (Service can have zero ports), but it won't route traffic.
	tmpl := &weavev1alpha1.WeaveServiceTemplate{
		Spec: weavev1alpha1.WeaveServiceTemplateSpec{Image: "myrepo/myapp:1.0"},
	}
	meta := &indexclient.AppMetadata{Runner: indexclient.AppRunner{Port: 0}}
	svc := deploybuilder.BuildServiceFromOverride(tmpl, meta, "run1", "step1", "fusion")
	if len(svc.Spec.Ports) != 0 {
		t.Errorf("expected no service ports, got %+v", svc.Spec.Ports)
	}
}

// ---- BuildIngressFromOverride ----

func TestBuildIngressFromOverride_NoIngressSpec_NoHost_ReturnsNil(t *testing.T) {
	tmpl := minTmpl("myrepo/myapp:1.0") // no Ingress spec
	ov := &weavev1alpha1.WeaveRunStepOverride{
		StepName: "step1", ArtifactName: "org.myapp", Tag: "stable",
		// IngressHost deliberately empty
	}
	ing := deploybuilder.BuildIngressFromOverride(tmpl, nil, ov, "run1", "step1", "fusion")
	if ing != nil {
		t.Errorf("expected nil ingress when no template ingress and no host, got %+v", ing)
	}
}

func TestBuildIngressFromOverride_HostOnly_DefaultPath(t *testing.T) {
	// No template ingress, no meta pathPrefix → Ingress is created with path "/".
	tmpl := minTmpl("myrepo/myapp:1.0")
	ov := &weavev1alpha1.WeaveRunStepOverride{
		StepName: "step1", ArtifactName: "org.myapp", Tag: "stable",
		IngressHost: "myapp.example.com",
	}
	ing := deploybuilder.BuildIngressFromOverride(tmpl, nil, ov, "run1", "step1", "fusion")
	if ing == nil {
		t.Fatal("expected ingress to be created when IngressHost is set")
	}
	if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != "myapp.example.com" {
		t.Errorf("host mismatch: %+v", ing.Spec.Rules)
	}
	path := ing.Spec.Rules[0].IngressRuleValue.HTTP.Paths[0].Path
	if path != "/" {
		t.Errorf("expected default path '/', got %q", path)
	}
}

func TestBuildIngressFromOverride_MetaPathPrefix_SetAsPath(t *testing.T) {
	// meta.Ingress.PathPrefix → path becomes "/" + pathPrefix.
	tmpl := minTmpl("myrepo/myapp:1.0")
	meta := &indexclient.AppMetadata{
		Ingress: indexclient.AppIngress{PathPrefix: "myapp"},
		Runner:  indexclient.AppRunner{Port: 8080},
	}
	ov := &weavev1alpha1.WeaveRunStepOverride{
		StepName: "step1", ArtifactName: "org.myapp", Tag: "stable",
		IngressHost: "myapp.example.com",
	}
	ing := deploybuilder.BuildIngressFromOverride(tmpl, meta, ov, "run1", "step1", "fusion")
	if ing == nil {
		t.Fatal("expected ingress to be created")
	}
	path := ing.Spec.Rules[0].IngressRuleValue.HTTP.Paths[0].Path
	if path != "/myapp" {
		t.Errorf("expected path '/myapp' from meta.Ingress.PathPrefix, got %q", path)
	}
	svcPort := ing.Spec.Rules[0].IngressRuleValue.HTTP.Paths[0].Backend.Service.Port.Number
	if svcPort != 8080 {
		t.Errorf("expected service port 8080 from meta, got %d", svcPort)
	}
}

func TestBuildIngressFromOverride_MetaPort_UsedAsServicePort(t *testing.T) {
	// meta.Runner.Port takes precedence over template port for the ingress backend.
	tmpl := minTmpl("myrepo/myapp:1.0") // template port 9000
	meta := &indexclient.AppMetadata{Runner: indexclient.AppRunner{Port: 8080}}
	ov := &weavev1alpha1.WeaveRunStepOverride{
		StepName: "step1", ArtifactName: "org.myapp", Tag: "stable",
		IngressHost: "myapp.example.com",
	}
	ing := deploybuilder.BuildIngressFromOverride(tmpl, meta, ov, "run1", "step1", "fusion")
	if ing == nil {
		t.Fatal("expected ingress to be created")
	}
	svcPort := ing.Spec.Rules[0].IngressRuleValue.HTTP.Paths[0].Backend.Service.Port.Number
	if svcPort != 8080 {
		t.Errorf("expected service port 8080 from meta.Runner.Port, got %d", svcPort)
	}
}

// ---- UpdateVersionEnvVar ----

func TestUpdateVersionEnvVar_UpdatesExisting(t *testing.T) {
	containers := []corev1.Container{{
		Name: "service",
		Env: []corev1.EnvVar{
			{Name: "OTHER", Value: "x"},
			{Name: "WEAVE_VERSION", Value: "1.0.0"},
			{Name: "ANOTHER", Value: "y"},
		},
	}}
	deploybuilder.UpdateVersionEnvVar(containers, "2.0.0")
	if v, ok := envVar(containers[0].Env, "WEAVE_VERSION"); !ok || v != "2.0.0" {
		t.Errorf("WEAVE_VERSION: got %q (found=%v), want 2.0.0", v, ok)
	}
	// Must not duplicate — still exactly one entry.
	count := 0
	for _, e := range containers[0].Env {
		if e.Name == "WEAVE_VERSION" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 WEAVE_VERSION after update, got %d", count)
	}
}

func TestUpdateVersionEnvVar_AppendsWhenAbsent(t *testing.T) {
	containers := []corev1.Container{{
		Name: "service",
		Env:  []corev1.EnvVar{{Name: "OTHER", Value: "x"}},
	}}
	deploybuilder.UpdateVersionEnvVar(containers, "3.0.0")
	if v, ok := envVar(containers[0].Env, "WEAVE_VERSION"); !ok || v != "3.0.0" {
		t.Errorf("WEAVE_VERSION: got %q (found=%v), want 3.0.0 (should be appended)", v, ok)
	}
	if len(containers[0].Env) != 2 {
		t.Errorf("expected 2 env vars after append, got %d", len(containers[0].Env))
	}
}

func TestUpdateVersionEnvVar_MultipleContainers(t *testing.T) {
	// All containers in the slice are updated regardless of whether they had the var.
	containers := []corev1.Container{
		{Name: "a", Env: []corev1.EnvVar{{Name: "WEAVE_VERSION", Value: "1.0.0"}}},
		{Name: "b", Env: []corev1.EnvVar{{Name: "OTHER", Value: "x"}}}, // absent
		{Name: "c", Env: []corev1.EnvVar{{Name: "WEAVE_VERSION", Value: "old"}}},
	}
	deploybuilder.UpdateVersionEnvVar(containers, "4.0.0")
	for i, c := range containers {
		if v, ok := envVar(c.Env, "WEAVE_VERSION"); !ok || v != "4.0.0" {
			t.Errorf("container[%d] %q WEAVE_VERSION: got %q (found=%v), want 4.0.0", i, c.Name, v, ok)
		}
	}
}

// ---- Runner.args collision with template env ----

func TestBuildFromOverride_RunnerArgsDuplicateEnvVar(t *testing.T) {
	// When runner.args contains a key already set in tmpl.Spec.Env, both entries
	// end up in the container env (template first, runner.args appended after).
	// Kubernetes does not deduplicate; the effective value depends on the runtime
	// (typically last-wins in Docker/containerd).
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.Env = []corev1.EnvVar{{Name: "LOG_LEVEL", Value: "prod"}}
	meta := &indexclient.AppMetadata{
		Runner: indexclient.AppRunner{
			Args: map[string]string{"LOG_LEVEL": "debug"},
		},
	}
	deploy := deploybuilder.BuildFromOverride(
		tmpl, minOverride("org.myapp", "stable"), meta,
		"run1", "step1", "fusion", security.Defaults{}, "1.0.0", "", "", nil,
	)
	c := deploy.Spec.Template.Spec.Containers[0]
	count := 0
	for _, e := range c.Env {
		if e.Name == "LOG_LEVEL" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 LOG_LEVEL entries (template + runner.args), got %d — builder does not deduplicate", count)
	}
}

// ---- BuildIngressFromOverride with TLS ----

func TestBuildIngressFromOverride_TLSFromTemplate_HostFromOverride(t *testing.T) {
	// TLSSecretName comes from the template; the TLS host list must use the
	// override host, not the template rule host.
	tmpl := minTmpl("myrepo/myapp:1.0")
	className := "nginx"
	tmpl.Spec.Ingress = &weavev1alpha1.WeaveIngressSpec{
		IngressClassName: &className,
		TLSSecretName:    "my-tls-secret",
		Rules: []weavev1alpha1.WeaveIngressRule{
			{Host: "template.example.com", Path: "/", ServicePort: "http"},
		},
	}
	ov := &weavev1alpha1.WeaveRunStepOverride{
		StepName: "step1", ArtifactName: "org.myapp", Tag: "stable",
		IngressHost: "override.example.com",
	}
	ing := deploybuilder.BuildIngressFromOverride(tmpl, nil, ov, "run1", "step1", "fusion")
	if ing == nil {
		t.Fatal("expected ingress to be created")
	}
	if len(ing.Spec.TLS) != 1 {
		t.Fatalf("expected 1 TLS entry, got %d", len(ing.Spec.TLS))
	}
	tls := ing.Spec.TLS[0]
	if tls.SecretName != "my-tls-secret" {
		t.Errorf("TLS secret: got %q, want my-tls-secret", tls.SecretName)
	}
	if len(tls.Hosts) != 1 || tls.Hosts[0] != "override.example.com" {
		t.Errorf("TLS hosts: got %v, want [override.example.com]", tls.Hosts)
	}
	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "nginx" {
		t.Errorf("ingressClassName: got %v, want nginx", ing.Spec.IngressClassName)
	}
}

// ---- Loader image resolution priority (BuildFromOverride) ----

func TestBuildFromOverride_LoaderImage_TemplateWins(t *testing.T) {
	// tmpl.Spec.CodeSource.LoaderImage takes priority over defaultLoaderImage.
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{
		ArtifactName: "org.myapp",
		Tag:          "stable",
		LoaderImage:  "custom-loader:1.0",
	}
	deploy := deploybuilder.BuildFromOverride(
		tmpl, minOverride("org.myapp", "stable"), nil,
		"run1", "step1", "fusion", security.Defaults{}, "1.0.0",
		"", "other-default:1.0", nil, // defaultLoaderImage should be ignored
	)
	inits := deploy.Spec.Template.Spec.InitContainers
	if len(inits) == 0 {
		t.Fatal("expected init container")
	}
	if inits[0].Image != "custom-loader:1.0" {
		t.Errorf("loader image: got %q, want custom-loader:1.0 (template should win)", inits[0].Image)
	}
}

func TestBuildFromOverride_LoaderImage_DefaultUsedWhenTemplateAbsent(t *testing.T) {
	// When template has no CodeSource (no LoaderImage), defaultLoaderImage is used.
	tmpl := minTmpl("myrepo/myapp:1.0") // no CodeSource
	deploy := deploybuilder.BuildFromOverride(
		tmpl, minOverride("org.myapp", "stable"), nil,
		"run1", "step1", "fusion", security.Defaults{}, "1.0.0",
		"", "my-default:2.0", nil,
	)
	inits := deploy.Spec.Template.Spec.InitContainers
	if len(inits) == 0 {
		t.Fatal("expected init container")
	}
	if inits[0].Image != "my-default:2.0" {
		t.Errorf("loader image: got %q, want my-default:2.0 (defaultLoaderImage should be used)", inits[0].Image)
	}
}

func TestBuildFromOverride_LoaderImage_HardcodedFallback(t *testing.T) {
	// When both template LoaderImage and defaultLoaderImage are empty, fall back
	// to the hardcoded "fusion-code-loader:latest".
	tmpl := minTmpl("myrepo/myapp:1.0") // no CodeSource
	deploy := deploybuilder.BuildFromOverride(
		tmpl, minOverride("org.myapp", "stable"), nil,
		"run1", "step1", "fusion", security.Defaults{}, "1.0.0",
		"", "", nil, // both empty
	)
	inits := deploy.Spec.Template.Spec.InitContainers
	if len(inits) == 0 {
		t.Fatal("expected init container")
	}
	if inits[0].Image != "fusion-code-loader:latest" {
		t.Errorf("loader image: got %q, want fusion-code-loader:latest (hardcoded fallback)", inits[0].Image)
	}
}

// ---- BuildService (chain-owned) ----

func TestBuildService_DefaultServiceType(t *testing.T) {
	// No ServiceType in template → defaults to ClusterIP.
	tmpl := minTmpl("myrepo/myapp:1.0")
	svc := deploybuilder.BuildService(tmpl, "chain", "step", "fusion")
	if svc.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Errorf("service type: got %q, want ClusterIP", svc.Spec.Type)
	}
}

func TestBuildService_ExplicitNodePortType(t *testing.T) {
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.ServiceType = corev1.ServiceTypeNodePort
	svc := deploybuilder.BuildService(tmpl, "chain", "step", "fusion")
	if svc.Spec.Type != corev1.ServiceTypeNodePort {
		t.Errorf("service type: got %q, want NodePort", svc.Spec.Type)
	}
}

func TestBuildService_TargetPort_FallsBackToPort(t *testing.T) {
	// TargetPort==0 in the template port spec → TargetPort in the service must
	// fall back to Port (effectiveTargetPort behaviour).
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.Ports = []weavev1alpha1.WeaveServicePort{
		{Name: "http", Port: 8080, TargetPort: 0, Protocol: corev1.ProtocolTCP},
	}
	svc := deploybuilder.BuildService(tmpl, "chain", "step", "fusion")
	if len(svc.Spec.Ports) != 1 {
		t.Fatalf("expected 1 service port, got %d", len(svc.Spec.Ports))
	}
	if svc.Spec.Ports[0].TargetPort.IntVal != 8080 {
		t.Errorf("TargetPort fallback: got %d, want 8080", svc.Spec.Ports[0].TargetPort.IntVal)
	}
}

func TestBuildService_TargetPort_ExplicitValue(t *testing.T) {
	// Explicit TargetPort != Port → service TargetPort uses the explicit value.
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.Ports = []weavev1alpha1.WeaveServicePort{
		{Name: "http", Port: 80, TargetPort: 8080, Protocol: corev1.ProtocolTCP},
	}
	svc := deploybuilder.BuildService(tmpl, "chain", "step", "fusion")
	if svc.Spec.Ports[0].Port != 80 {
		t.Errorf("Port: got %d, want 80", svc.Spec.Ports[0].Port)
	}
	if svc.Spec.Ports[0].TargetPort.IntVal != 8080 {
		t.Errorf("TargetPort: got %d, want 8080", svc.Spec.Ports[0].TargetPort.IntVal)
	}
}

// ---- BuildIngress (chain-owned) / resolveServicePort ----

// tmplWithIngressRules is a helper that builds a template with a configured Ingress spec.
func tmplWithIngressRules(ports []weavev1alpha1.WeaveServicePort, rules []weavev1alpha1.WeaveIngressRule) *weavev1alpha1.WeaveServiceTemplate {
	return &weavev1alpha1.WeaveServiceTemplate{
		Spec: weavev1alpha1.WeaveServiceTemplateSpec{
			Image:  "myrepo/myapp:1.0",
			Ports:  ports,
			Ingress: &weavev1alpha1.WeaveIngressSpec{Rules: rules},
		},
	}
}

func TestBuildIngress_NilIngressSpec_ReturnsNil(t *testing.T) {
	tmpl := minTmpl("myrepo/myapp:1.0") // no Ingress spec
	if ing := deploybuilder.BuildIngress(tmpl, "chain", "step", "fusion"); ing != nil {
		t.Errorf("expected nil when template has no ingress spec, got %+v", ing)
	}
}

func TestBuildIngress_ServicePortByName(t *testing.T) {
	// ServicePort ref matches a declared port name → backend uses Name form (not Number).
	tmpl := tmplWithIngressRules(
		[]weavev1alpha1.WeaveServicePort{{Name: "http", Port: 8080}},
		[]weavev1alpha1.WeaveIngressRule{{Host: "foo.com", Path: "/", ServicePort: "http"}},
	)
	ing := deploybuilder.BuildIngress(tmpl, "chain", "step", "fusion")
	if ing == nil {
		t.Fatal("expected ingress to be created")
	}
	port := ing.Spec.Rules[0].IngressRuleValue.HTTP.Paths[0].Backend.Service.Port
	if port.Name != "http" {
		t.Errorf("backend port name: got %q, want http", port.Name)
	}
	if port.Number != 0 {
		t.Errorf("backend port number should be 0 for name-based ref, got %d", port.Number)
	}
}

func TestBuildIngress_ServicePortNumeric(t *testing.T) {
	// ServicePort ref is a numeric string not matching any port name → numeric backend port.
	tmpl := tmplWithIngressRules(
		[]weavev1alpha1.WeaveServicePort{{Name: "http", Port: 8080}},
		[]weavev1alpha1.WeaveIngressRule{{Host: "foo.com", Path: "/", ServicePort: "9000"}},
	)
	ing := deploybuilder.BuildIngress(tmpl, "chain", "step", "fusion")
	if ing == nil {
		t.Fatal("expected ingress to be created")
	}
	port := ing.Spec.Rules[0].IngressRuleValue.HTTP.Paths[0].Backend.Service.Port
	if port.Number != 9000 {
		t.Errorf("backend port number: got %d, want 9000", port.Number)
	}
	if port.Name != "" {
		t.Errorf("backend port name should be empty for numeric ref, got %q", port.Name)
	}
}

func TestBuildIngress_ServicePortUnresolvable(t *testing.T) {
	// ServicePort ref is neither a declared name nor a numeric string → falls back
	// to treating the ref itself as a named port reference.
	tmpl := tmplWithIngressRules(
		[]weavev1alpha1.WeaveServicePort{{Name: "http", Port: 8080}},
		[]weavev1alpha1.WeaveIngressRule{{Host: "foo.com", Path: "/", ServicePort: "nonexistent"}},
	)
	ing := deploybuilder.BuildIngress(tmpl, "chain", "step", "fusion")
	if ing == nil {
		t.Fatal("expected ingress to be created")
	}
	port := ing.Spec.Rules[0].IngressRuleValue.HTTP.Paths[0].Backend.Service.Port
	if port.Name != "nonexistent" {
		t.Errorf("backend port name: got %q, want nonexistent (unresolvable ref used as name)", port.Name)
	}
	if port.Number != 0 {
		t.Errorf("backend port number should be 0 for name-based fallback, got %d", port.Number)
	}
}

func TestBuild_WritablePaths_MountsEmptyDirsInBothContainers(t *testing.T) {
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.app", Tag: "stable"}
	paths := []string{"/tmp", "/home/nonroot", "/weave-work"}
	deploy := deploybuilder.Build(tmpl, "chain", "step", "fusion", security.Defaults{}, nil, "", "", "", paths)

	// Check volumes exist for each writable path.
	volNames := map[string]bool{}
	for _, v := range deploy.Spec.Template.Spec.Volumes {
		volNames[v.Name] = true
	}
	for _, name := range []string{"weave-w-tmp", "weave-w-home-nonroot", "weave-w-weave-work"} {
		if !volNames[name] {
			t.Errorf("expected volume %q", name)
		}
	}

	// Main container must have the mounts.
	mainMounts := map[string]bool{}
	for _, m := range deploy.Spec.Template.Spec.Containers[0].VolumeMounts {
		mainMounts[m.MountPath] = true
	}
	for _, p := range paths {
		if !mainMounts[p] {
			t.Errorf("main container missing mount at %q", p)
		}
	}

	// Init container must also have the mounts.
	initMounts := map[string]bool{}
	for _, m := range deploy.Spec.Template.Spec.InitContainers[0].VolumeMounts {
		initMounts[m.MountPath] = true
	}
	for _, p := range paths {
		if !initMounts[p] {
			t.Errorf("init container missing mount at %q", p)
		}
	}
}

func TestBuild_NilWritablePaths_NoExtraVolumes(t *testing.T) {
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.app", Tag: "stable"}
	deploy := deploybuilder.Build(tmpl, "chain", "step", "fusion", security.Defaults{}, nil, "", "", "", nil)

	for _, v := range deploy.Spec.Template.Spec.Volumes {
		if v.Name != "weave-code" && strings.HasPrefix(v.Name, "weave-w") {
			t.Errorf("unexpected writable volume %q with nil writablePaths", v.Name)
		}
	}
}

func TestBuild_WritablePaths_SanitizesName(t *testing.T) {
	// Uppercase, underscore, and dot must be lowercased/replaced; the result must be a valid DNS label.
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.app", Tag: "stable"}
	deploy := deploybuilder.Build(tmpl, "chain", "step", "fusion", security.Defaults{}, nil, "", "", "",
		[]string{"/My_Path", "/var/log.d"})

	volNames := map[string]bool{}
	for _, v := range deploy.Spec.Template.Spec.Volumes {
		volNames[v.Name] = true
	}
	if !volNames["weave-w-my-path"] {
		t.Errorf("expected sanitized volume name weave-w-my-path, got %v", volNames)
	}
	if !volNames["weave-w-var-log-d"] {
		t.Errorf("expected sanitized volume name weave-w-var-log-d, got %v", volNames)
	}
}

func TestBuild_WritablePaths_DeduplicatesCollisions(t *testing.T) {
	// /a-b and /a/b produce the same slug → only one volume should be created.
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.app", Tag: "stable"}
	deploy := deploybuilder.Build(tmpl, "chain", "step", "fusion", security.Defaults{}, nil, "", "", "",
		[]string{"/a-b", "/a/b"})

	count := 0
	for _, v := range deploy.Spec.Template.Spec.Volumes {
		if v.Name == "weave-w-a-b" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 volume weave-w-a-b after dedup, got %d", count)
	}
}

func TestBuild_WritablePaths_SkipsUserVolumeCollision(t *testing.T) {
	// A user-defined volume named "weave-w-tmp" must not produce a duplicate.
	tmpl := minTmpl("myrepo/myapp:1.0")
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.app", Tag: "stable"}
	tmpl.Spec.Volumes = []weavev1alpha1.WeaveVolumeMount{{Name: "weave-w-tmp", SecretName: "some-secret", MountPath: "/secret"}}
	deploy := deploybuilder.Build(tmpl, "chain", "step", "fusion", security.Defaults{}, nil, "", "", "",
		[]string{"/tmp"})

	count := 0
	for _, v := range deploy.Spec.Template.Spec.Volumes {
		if v.Name == "weave-w-tmp" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 volume weave-w-tmp (user-defined, not duplicated), got %d", count)
	}
}
