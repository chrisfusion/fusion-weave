// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/indexclient"
)

func emptyDirAt(t *testing.T, pod *corev1.PodSpec, container corev1.Container, mountPath string) *corev1.EmptyDirVolumeSource {
	t.Helper()
	for _, m := range container.VolumeMounts {
		if m.MountPath != mountPath {
			continue
		}
		for _, v := range pod.Volumes {
			if v.Name == m.Name && v.EmptyDir != nil {
				return v.EmptyDir
			}
		}
	}
	t.Fatalf("container %q has no emptyDir at %s (mounts %+v)", container.Name, mountPath, container.VolumeMounts)
	return nil
}

func TestApplyWritablePaths(t *testing.T) {
	limit := resource.MustParse("100Mi")
	// /tmp is already mounted by the operator-wide WRITABLE_PATHS.
	pod := &corev1.PodSpec{
		Volumes: []corev1.Volume{{Name: "weave-w-tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		InitContainers: []corev1.Container{{Name: "code-loader",
			VolumeMounts: []corev1.VolumeMount{{Name: "weave-w-tmp", MountPath: "/tmp"}}}},
		Containers: []corev1.Container{{Name: "service",
			VolumeMounts: []corev1.VolumeMount{{Name: "weave-w-tmp", MountPath: "/tmp"}}}},
	}
	applyWritablePaths(pod, []weavev1alpha1.WeaveAppWritablePath{
		{MountPath: "/tmp", SizeLimit: &limit},
		{MountPath: "/var/cache/nginx/", Medium: corev1.StorageMediumMemory},
		{MountPath: "/a-b"},
		{MountPath: "/a/b"}, // sanitizes to the same volume name as /a-b
	})

	if len(pod.Volumes) != 4 {
		t.Fatalf("volumes: %+v", pod.Volumes)
	}
	if ed := emptyDirAt(t, pod, pod.Containers[0], "/tmp"); ed.SizeLimit == nil || ed.SizeLimit.String() != "100Mi" {
		t.Errorf("/tmp should only get the sizeLimit: %+v", ed)
	}
	for _, c := range []corev1.Container{pod.Containers[0], pod.InitContainers[0]} {
		if ed := emptyDirAt(t, pod, c, "/var/cache/nginx"); ed.Medium != corev1.StorageMediumMemory {
			t.Errorf("%s: medium %q", c.Name, ed.Medium)
		}
		emptyDirAt(t, pod, c, "/a-b")
		emptyDirAt(t, pod, c, "/a/b")
	}
	if pod.Volumes[2].Name == pod.Volumes[3].Name {
		t.Errorf("colliding volume names: %s", pod.Volumes[2].Name)
	}
}

func TestApplyWritablePaths_LeavesNonEmptyDirMountAlone(t *testing.T) {
	pod := &corev1.PodSpec{
		Volumes: []corev1.Volume{{Name: "cfg", VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}}}}},
		Containers: []corev1.Container{{Name: "job", VolumeMounts: []corev1.VolumeMount{{Name: "cfg", MountPath: "/etc/app"}}}},
	}
	applyWritablePaths(pod, []weavev1alpha1.WeaveAppWritablePath{{MountPath: "/etc/app"}})
	if len(pod.Volumes) != 1 || len(pod.Containers[0].VolumeMounts) != 1 {
		t.Errorf("configmap mount must not be shadowed: %+v", pod)
	}
}

func TestAppWritablePaths_RunnerTypeThenApp(t *testing.T) {
	runner := map[string][]string{"streamlit": {"/home/nonroot/.streamlit"}, "python": {"/other"}}
	spec := &weavev1alpha1.WeaveAppSpec{WritablePaths: []weavev1alpha1.WeaveAppWritablePath{{MountPath: "/data"}}}
	meta := &indexclient.AppMetadata{Runner: indexclient.AppRunner{Type: "streamlit"}}

	got := appWritablePaths(runner, meta, spec)
	if len(got) != 2 || got[0].MountPath != "/home/nonroot/.streamlit" || got[1].MountPath != "/data" {
		t.Errorf("with metadata: %+v", got)
	}
	if got := appWritablePaths(runner, nil, spec); len(got) != 1 || got[0].MountPath != "/data" {
		t.Errorf("without metadata only the app's entries apply: %+v", got)
	}
}

func TestValidateAppWritablePaths(t *testing.T) {
	cases := map[string]struct {
		spec weavev1alpha1.WeaveAppSpec
		want string
	}{
		"relative": {weavev1alpha1.WeaveAppSpec{WritablePaths: []weavev1alpha1.WeaveAppWritablePath{{MountPath: "tmp"}}}, "absolute"},
		"root":     {weavev1alpha1.WeaveAppSpec{WritablePaths: []weavev1alpha1.WeaveAppWritablePath{{MountPath: "/"}}}, "absolute"},
		"duplicate": {weavev1alpha1.WeaveAppSpec{WritablePaths: []weavev1alpha1.WeaveAppWritablePath{
			{MountPath: "/data"}, {MountPath: "/data/"}}}, "duplicate"},
		"volume": {weavev1alpha1.WeaveAppSpec{
			Volumes:       []weavev1alpha1.WeaveVolumeMount{{Name: "cfg", MountPath: "/etc/app", ConfigMapName: "cfg"}},
			WritablePaths: []weavev1alpha1.WeaveAppWritablePath{{MountPath: "/etc/app"}}}, `volume "cfg"`},
		"code mount": {weavev1alpha1.WeaveAppSpec{
			CodeSource:    &weavev1alpha1.CodeSourceSpec{ArtifactName: "a", Tag: "stable"},
			WritablePaths: []weavev1alpha1.WeaveAppWritablePath{{MountPath: "/weave-code"}}}, "codeSource.mountPath"},
		"ok": {weavev1alpha1.WeaveAppSpec{WritablePaths: []weavev1alpha1.WeaveAppWritablePath{
			{MountPath: "/tmp"}, {MountPath: "/var/run"}}}, ""},
	}
	for name, tc := range cases {
		got := validateAppWritablePaths(&tc.spec)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: got %q, want containing %q", name, got, tc.want)
		}
	}
}

func TestWeaveAppService_WritablePathsWithoutCodeSource(t *testing.T) {
	app := serviceApp("site")
	app.Spec.WritablePaths = []weavev1alpha1.WeaveAppWritablePath{{MountPath: "/var/cache/nginx"}, {MountPath: "/var/run"}}
	r := newAppReconciler(t, app)
	r.WritablePaths = []string{"/tmp"} // operator-wide paths only apply with codeSource
	reconcileApp(t, r, "site")

	var d appsv1.Deployment
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: "site-app"}, &d); err != nil {
		t.Fatal(err)
	}
	pod := &d.Spec.Template.Spec
	emptyDirAt(t, pod, pod.Containers[0], "/var/cache/nginx")
	emptyDirAt(t, pod, pod.Containers[0], "/var/run")
	if len(pod.Volumes) != 2 {
		t.Errorf("volumes: %+v", pod.Volumes)
	}
}

func TestWeaveAppJob_WritablePathsMountedIntoJob(t *testing.T) {
	limit := resource.MustParse("50Mi")
	app := jobApp("etl")
	app.Spec.WritablePaths = []weavev1alpha1.WeaveAppWritablePath{{MountPath: "/scratch", SizeLimit: &limit}}
	r := newAppReconciler(t, app)
	reconcileApp(t, r, "etl")
	fireAnnotated(t, r, "etl", "adhoc")
	reconcileApp(t, r, "etl")

	jobs := listAppJobs(t, r)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	pod := &jobs[0].Spec.Template.Spec
	if ed := emptyDirAt(t, pod, pod.Containers[0], "/scratch"); ed.SizeLimit == nil || ed.SizeLimit.String() != "50Mi" {
		t.Errorf("sizeLimit: %+v", ed)
	}
}

func TestWeaveAppInvalidWritablePath_SetsInvalid(t *testing.T) {
	app := serviceApp("site")
	app.Spec.WritablePaths = []weavev1alpha1.WeaveAppWritablePath{{MountPath: "/data"}, {MountPath: "/data"}}
	r := newAppReconciler(t, app)
	got := reconcileApp(t, r, "site")
	if got.Status.Phase != weavev1alpha1.WeaveAppPhaseInvalid || !strings.Contains(got.Status.ValidationMessage, "duplicate") {
		t.Errorf("phase %q, message %q", got.Status.Phase, got.Status.ValidationMessage)
	}
}
