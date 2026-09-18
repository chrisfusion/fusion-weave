// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package jobbuilder_test

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/indexclient"
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

// envVar looks up an env var by name in a slice.
func envVar(env []corev1.EnvVar, name string) (string, bool) {
	for _, e := range env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func TestBuild_AuthSecretName_SetsEnvFrom(t *testing.T) {
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(minJobTmpl(), step, minRun(), 0, "", "", security.Defaults{}, "my-auth-secret", nil, "", "", "", nil, true, nil, "")
	c := job.Spec.Template.Spec.Containers[0]
	if len(c.EnvFrom) != 1 || c.EnvFrom[0].SecretRef == nil || c.EnvFrom[0].SecretRef.Name != "my-auth-secret" {
		t.Errorf("expected envFrom secretRef my-auth-secret, got %+v", c.EnvFrom)
	}
}

func TestBuild_EmptyAuthSecretName_NoEnvFrom(t *testing.T) {
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(minJobTmpl(), step, minRun(), 0, "", "", security.Defaults{}, "", nil, "", "", "", nil, true, nil, "")
	c := job.Spec.Template.Spec.Containers[0]
	if len(c.EnvFrom) != 0 {
		t.Errorf("expected no envFrom when authSecretName is empty, got %+v", c.EnvFrom)
	}
}

// ---- codeSource ----

func TestBuild_NoCodeSource_NoInitContainers(t *testing.T) {
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(minJobTmpl(), step, minRun(), 0, "", "", security.Defaults{}, "", nil, "", "", "", nil, true, nil, "")
	if len(job.Spec.Template.Spec.InitContainers) != 0 {
		t.Errorf("expected no init containers without codeSource, got %d", len(job.Spec.Template.Spec.InitContainers))
	}
}

func TestBuild_CodeSource_DefaultMountPath(t *testing.T) {
	tmpl := minJobTmpl()
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{
		ArtifactName: "org.myteam.myapp",
		Tag:          "stable",
		// MountPath deliberately empty
	}
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(tmpl, step, minRun(), 0, "", "", security.Defaults{}, "", nil, "", "", "", nil, true, nil, "")
	inits := job.Spec.Template.Spec.InitContainers
	if len(inits) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(inits))
	}
	if inits[0].Name != "code-loader" || inits[0].Command[0] != "/loader" {
		t.Errorf("unexpected init container: %+v", inits[0])
	}
	if v, ok := envVar(inits[0].Env, "MOUNT_PATH"); !ok || v != "/weave-code" {
		t.Errorf("MOUNT_PATH: got %q (found=%v), want /weave-code", v, ok)
	}

	// main container must mount weave-code too.
	found := false
	for _, m := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		if m.Name == "weave-code" && m.MountPath == "/weave-code" {
			found = true
		}
	}
	if !found {
		t.Error("main container missing weave-code mount")
	}
}

func TestBuild_NilMeta_WithCodeSource_BaseVarsOnly(t *testing.T) {
	tmpl := minJobTmpl()
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.myteam.myapp", Tag: "stable"}
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(tmpl, step, minRun(), 0, "", "", security.Defaults{}, "", nil, "1.0.0", "", "", nil, true, nil, "")
	c := job.Spec.Template.Spec.Containers[0]

	if v, ok := envVar(c.Env, "WEAVE_ARTIFACT"); !ok || v != "org.myteam.myapp" {
		t.Errorf("WEAVE_ARTIFACT: got %q (found=%v)", v, ok)
	}
	if v, ok := envVar(c.Env, "WEAVE_VERSION"); !ok || v != "1.0.0" {
		t.Errorf("WEAVE_VERSION: got %q (found=%v)", v, ok)
	}
	if v, ok := envVar(c.Env, "WEAVE_NAMESPACE"); !ok || v != "fusion" {
		t.Errorf("WEAVE_NAMESPACE: got %q (found=%v)", v, ok)
	}
	if _, ok := envVar(c.Env, "WEAVE_RUNNER_TYPE"); ok {
		t.Error("WEAVE_RUNNER_TYPE must not be set when meta is nil")
	}
}

func TestBuild_MetaRunnerArgs_InjectedAsEnvVars(t *testing.T) {
	tmpl := minJobTmpl()
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.app", Tag: "stable"}
	meta := &indexclient.AppMetadata{Runner: indexclient.AppRunner{Type: "python", Args: map[string]string{"ENTRYPOINT": "app.py"}}}
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(tmpl, step, minRun(), 0, "", "", security.Defaults{}, "", meta, "1.0.0", "", "", nil, true, nil, "")
	c := job.Spec.Template.Spec.Containers[0]
	if v, ok := envVar(c.Env, "WEAVE_RUNNER_TYPE"); !ok || v != "python" {
		t.Errorf("WEAVE_RUNNER_TYPE: got %q (found=%v)", v, ok)
	}
	if v, ok := envVar(c.Env, "ENTRYPOINT"); !ok || v != "app.py" {
		t.Errorf("ENTRYPOINT: got %q (found=%v)", v, ok)
	}
}

func TestBuild_CodeSource_LoaderImage_DefaultFallback(t *testing.T) {
	tmpl := minJobTmpl()
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.app", Tag: "stable"}
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(tmpl, step, minRun(), 0, "", "", security.Defaults{}, "", nil, "", "", "", nil, true, nil, "")
	if img := job.Spec.Template.Spec.InitContainers[0].Image; img != "fusion-code-loader:latest" {
		t.Errorf("expected hardcoded fallback loader image, got %q", img)
	}
}

func TestBuild_CodeSource_LoaderImage_OperatorDefaultUsedWhenTemplateAbsent(t *testing.T) {
	tmpl := minJobTmpl()
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.app", Tag: "stable"}
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(tmpl, step, minRun(), 0, "", "", security.Defaults{}, "", nil, "", "", "operator-default:latest", nil, true, nil, "")
	if img := job.Spec.Template.Spec.InitContainers[0].Image; img != "operator-default:latest" {
		t.Errorf("expected operator default loader image, got %q", img)
	}
}

func TestBuild_CodeSource_LoaderImage_TemplateWins(t *testing.T) {
	tmpl := minJobTmpl()
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.app", Tag: "stable", LoaderImage: "template-loader:v2"}
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(tmpl, step, minRun(), 0, "", "", security.Defaults{}, "", nil, "", "", "operator-default:latest", nil, true, nil, "")
	if img := job.Spec.Template.Spec.InitContainers[0].Image; img != "template-loader:v2" {
		t.Errorf("expected template loader image to win, got %q", img)
	}
}

// ---- writable paths ----

func TestBuild_WritablePaths_MountsEmptyDirsInBothContainers(t *testing.T) {
	tmpl := minJobTmpl()
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.app", Tag: "stable"}
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	paths := []string{"/tmp", "/home/nonroot", "/weave-work"}
	job := jobbuilder.Build(tmpl, step, minRun(), 0, "", "", security.Defaults{}, "", nil, "", "", "", paths, true, nil, "")

	volNames := map[string]bool{}
	for _, v := range job.Spec.Template.Spec.Volumes {
		volNames[v.Name] = true
	}
	for _, name := range []string{"weave-w-tmp", "weave-w-home-nonroot", "weave-w-weave-work"} {
		if !volNames[name] {
			t.Errorf("expected volume %q", name)
		}
	}

	mainMounts := map[string]bool{}
	for _, m := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		mainMounts[m.MountPath] = true
	}
	for _, p := range paths {
		if !mainMounts[p] {
			t.Errorf("main container missing mount at %q", p)
		}
	}

	initMounts := map[string]bool{}
	for _, m := range job.Spec.Template.Spec.InitContainers[0].VolumeMounts {
		initMounts[m.MountPath] = true
	}
	for _, p := range paths {
		if !initMounts[p] {
			t.Errorf("init container missing mount at %q", p)
		}
	}
}

func TestBuild_NilWritablePaths_NoExtraVolumes(t *testing.T) {
	tmpl := minJobTmpl()
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.app", Tag: "stable"}
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(tmpl, step, minRun(), 0, "", "", security.Defaults{}, "", nil, "", "", "", nil, true, nil, "")

	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name != "weave-code" && strings.HasPrefix(v.Name, "weave-w") {
			t.Errorf("unexpected writable volume %q with nil writablePaths", v.Name)
		}
	}
}

func TestBuild_WritablePaths_NoCodeSource_NoVolumes(t *testing.T) {
	// writablePaths mounting is gated on codeSource being configured, same as deploybuilder.
	tmpl := minJobTmpl()
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(tmpl, step, minRun(), 0, "", "", security.Defaults{}, "", nil, "", "", "", []string{"/tmp"}, true, nil, "")

	for _, v := range job.Spec.Template.Spec.Volumes {
		if strings.HasPrefix(v.Name, "weave-w") {
			t.Errorf("unexpected writable volume %q without codeSource", v.Name)
		}
	}
}

func TestBuild_WritablePaths_DeduplicatesCollisions(t *testing.T) {
	tmpl := minJobTmpl()
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.app", Tag: "stable"}
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(tmpl, step, minRun(), 0, "", "", security.Defaults{}, "", nil, "", "", "",
		[]string{"/a-b", "/a/b"}, true, nil, "")

	count := 0
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "weave-w-a-b" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 volume weave-w-a-b after dedup, got %d", count)
	}
}

func TestBuild_WritablePaths_SkipsUserVolumeCollision(t *testing.T) {
	tmpl := minJobTmpl()
	tmpl.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "org.app", Tag: "stable"}
	tmpl.Spec.Volumes = []weavev1alpha1.WeaveVolumeMount{{Name: "weave-w-tmp", SecretName: "some-secret", MountPath: "/secret"}}
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(tmpl, step, minRun(), 0, "", "", security.Defaults{}, "", nil, "", "", "",
		[]string{"/tmp"}, true, nil, "")

	count := 0
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "weave-w-tmp" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 volume weave-w-tmp (user-defined, not duplicated), got %d", count)
	}
}

// ---- AuthSecretRef file mount + UnsafeEnvironmentInjector ----

func hasVolume(volumes []corev1.Volume, name string) bool {
	for _, v := range volumes {
		if v.Name == name {
			return true
		}
	}
	return false
}

func hasMount(mounts []corev1.VolumeMount, name string) bool {
	for _, m := range mounts {
		if m.Name == name {
			return true
		}
	}
	return false
}

func TestBuild_AuthSecretName_UnsafeInjectorFalse_NoEnvFromButFileMounted(t *testing.T) {
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(minJobTmpl(), step, minRun(), 0, "", "", security.Defaults{}, "my-auth-secret", nil, "", "", "", nil, false, nil, "")
	c := job.Spec.Template.Spec.Containers[0]
	if len(c.EnvFrom) != 0 {
		t.Errorf("expected no envFrom when unsafeEnvironmentInjector is false, got %+v", c.EnvFrom)
	}
	if !hasVolume(job.Spec.Template.Spec.Volumes, "weave-auth-secret") || !hasMount(c.VolumeMounts, "weave-auth-secret") {
		t.Error("expected weave-auth-secret volume/mount regardless of unsafeEnvironmentInjector")
	}
	if v, ok := envVar(c.Env, "WEAVE_AUTH_SECRET_DIR"); !ok || v != "/var/run/secrets/fusion-platform.io/auth-secret" {
		t.Errorf("WEAVE_AUTH_SECRET_DIR: got %q (found=%v)", v, ok)
	}
}

func TestBuild_AuthSecretName_UnsafeInjectorTrue_FileStillMounted(t *testing.T) {
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(minJobTmpl(), step, minRun(), 0, "", "", security.Defaults{}, "my-auth-secret", nil, "", "", "", nil, true, nil, "")
	c := job.Spec.Template.Spec.Containers[0]
	if len(c.EnvFrom) != 1 {
		t.Errorf("expected envFrom set when unsafeEnvironmentInjector is true, got %+v", c.EnvFrom)
	}
	if !hasVolume(job.Spec.Template.Spec.Volumes, "weave-auth-secret") {
		t.Error("expected weave-auth-secret volume even when envFrom is also set")
	}
}

// ---- ExternalAuthRef ----

func TestBuild_ExternalAuthRef_MountsFileAndDiscoveryVars_NoTokenEnvWhenSafe(t *testing.T) {
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	ref := &weavev1alpha1.WeaveExternalAuthRef{Mode: weavev1alpha1.ExternalAuthModeServiceAccount, Name: "demo-sa"}
	job := jobbuilder.Build(minJobTmpl(), step, minRun(), 0, "", "", security.Defaults{}, "", nil, "", "", "", nil, false, ref, "run1-step1-0-ext-auth")
	c := job.Spec.Template.Spec.Containers[0]

	if !hasVolume(job.Spec.Template.Spec.Volumes, "weave-external-auth") || !hasMount(c.VolumeMounts, "weave-external-auth") {
		t.Error("expected weave-external-auth volume/mount")
	}
	if v, ok := envVar(c.Env, "WEAVE_EXTERNAL_AUTH_TOKEN_FILE"); !ok || v != "/var/run/secrets/fusion-platform.io/external-auth/token" {
		t.Errorf("WEAVE_EXTERNAL_AUTH_TOKEN_FILE: got %q (found=%v)", v, ok)
	}
	if v, ok := envVar(c.Env, "WEAVE_EXTERNAL_AUTH_MODE"); !ok || v != "serviceAccount" {
		t.Errorf("WEAVE_EXTERNAL_AUTH_MODE: got %q (found=%v)", v, ok)
	}
	if v, ok := envVar(c.Env, "WEAVE_EXTERNAL_AUTH_NAME"); !ok || v != "demo-sa" {
		t.Errorf("WEAVE_EXTERNAL_AUTH_NAME: got %q (found=%v)", v, ok)
	}
	if _, ok := envVar(c.Env, "WEAVE_EXTERNAL_AUTH_TOKEN"); ok {
		t.Error("WEAVE_EXTERNAL_AUTH_TOKEN must not be set when unsafeEnvironmentInjector is false")
	}
}

func TestBuild_ExternalAuthRef_UnsafeInjectorTrue_SetsTokenEnvViaSecretKeyRef(t *testing.T) {
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	ref := &weavev1alpha1.WeaveExternalAuthRef{Mode: weavev1alpha1.ExternalAuthModeOIDC, Name: "demo-oidc"}
	job := jobbuilder.Build(minJobTmpl(), step, minRun(), 0, "", "", security.Defaults{}, "", nil, "", "", "", nil, true, ref, "run1-step1-0-ext-auth")
	c := job.Spec.Template.Spec.Containers[0]

	var found *corev1.EnvVar
	for i := range c.Env {
		if c.Env[i].Name == "WEAVE_EXTERNAL_AUTH_TOKEN" {
			found = &c.Env[i]
		}
	}
	if found == nil {
		t.Fatal("expected WEAVE_EXTERNAL_AUTH_TOKEN to be set")
	}
	if found.Value != "" {
		t.Errorf("expected WEAVE_EXTERNAL_AUTH_TOKEN to be sourced via SecretKeyRef, not a literal value, got %q", found.Value)
	}
	if found.ValueFrom == nil || found.ValueFrom.SecretKeyRef == nil ||
		found.ValueFrom.SecretKeyRef.Name != "run1-step1-0-ext-auth" || found.ValueFrom.SecretKeyRef.Key != "token" {
		t.Errorf("expected WEAVE_EXTERNAL_AUTH_TOKEN sourced from secretKeyRef{name: run1-step1-0-ext-auth, key: token}, got %+v", found.ValueFrom)
	}
}

func TestBuild_NilExternalAuthRef_NoExternalAuthVolumes(t *testing.T) {
	step := &weavev1alpha1.WeaveChainStep{Name: "step1"}
	job := jobbuilder.Build(minJobTmpl(), step, minRun(), 0, "", "", security.Defaults{}, "", nil, "", "", "", nil, true, nil, "")
	if hasVolume(job.Spec.Template.Spec.Volumes, "weave-external-auth") {
		t.Error("expected no weave-external-auth volume when externalAuthRef is nil")
	}
	c := job.Spec.Template.Spec.Containers[0]
	if _, ok := envVar(c.Env, "WEAVE_EXTERNAL_AUTH_TOKEN_FILE"); ok {
		t.Error("expected no WEAVE_EXTERNAL_AUTH_TOKEN_FILE when externalAuthRef is nil")
	}
}

func TestExternalAuthSecretName(t *testing.T) {
	if got := jobbuilder.ExternalAuthSecretName("run1-step1-0"); got != "run1-step1-0-ext-auth" {
		t.Errorf("got %q, want run1-step1-0-ext-auth", got)
	}
}
