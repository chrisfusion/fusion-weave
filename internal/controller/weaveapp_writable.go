// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"fmt"
	"path"
	"strconv"

	corev1 "k8s.io/api/core/v1"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/codesource"
	"fusion-platform.io/fusion-weave/internal/indexclient"
)

// validateAppWritablePaths rejects relative, root and duplicate paths and paths
// that collide with the code mount or a declared spec.volumes mount.
func validateAppWritablePaths(spec *weavev1alpha1.WeaveAppSpec) string {
	taken := map[string]string{}
	for _, v := range spec.Volumes {
		taken[path.Clean(v.MountPath)] = fmt.Sprintf("volume %q", v.Name)
	}
	if cs := spec.CodeSource; cs != nil {
		mp := cs.MountPath
		if mp == "" {
			mp = "/weave-code"
		}
		taken[path.Clean(mp)] = "codeSource.mountPath"
	}
	seen := map[string]bool{}
	for _, wp := range spec.WritablePaths {
		p := path.Clean(wp.MountPath)
		if !path.IsAbs(p) || p == "/" {
			return fmt.Sprintf("writablePaths: %q must be an absolute path other than /", wp.MountPath)
		}
		if seen[p] {
			return fmt.Sprintf("writablePaths: duplicate path %q", p)
		}
		seen[p] = true
		if by, ok := taken[p]; ok {
			return fmt.Sprintf("writablePaths: %q is already mounted by %s", p, by)
		}
	}
	return ""
}

// appWritablePaths returns the runner-type defaults (by the artifact's
// metadata.yaml runner.type) followed by the app's own entries.
func appWritablePaths(runnerPaths map[string][]string, meta *indexclient.AppMetadata, spec *weavev1alpha1.WeaveAppSpec) []weavev1alpha1.WeaveAppWritablePath {
	var out []weavev1alpha1.WeaveAppWritablePath
	if meta != nil {
		for _, p := range runnerPaths[meta.Runner.Type] {
			out = append(out, weavev1alpha1.WeaveAppWritablePath{MountPath: p})
		}
	}
	return append(out, spec.WritablePaths...)
}

// applyWritablePaths mounts a writable emptyDir at each path into every
// container and init container of pod. A path that is already mounted from an
// emptyDir (operator WRITABLE_PATHS or an earlier entry) only gets the entry's
// sizeLimit/medium; one mounted from anything else is left alone.
func applyWritablePaths(pod *corev1.PodSpec, paths []weavev1alpha1.WeaveAppWritablePath) {
	if len(pod.Containers) == 0 {
		return
	}
	for _, wp := range paths {
		p := path.Clean(wp.MountPath)
		if existing := mountedVolume(pod, p); existing != nil {
			if existing.EmptyDir != nil {
				if wp.SizeLimit != nil {
					existing.EmptyDir.SizeLimit = wp.SizeLimit
				}
				if wp.Medium != "" {
					existing.EmptyDir.Medium = wp.Medium
				}
			}
			continue
		}
		name := uniqueVolumeName(pod.Volumes, codesource.WritableVolumeName(p))
		if name == "" {
			continue
		}
		pod.Volumes = append(pod.Volumes, corev1.Volume{
			Name: name,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
				Medium: wp.Medium, SizeLimit: wp.SizeLimit,
			}},
		})
		vm := corev1.VolumeMount{Name: name, MountPath: p}
		for i := range pod.Containers {
			pod.Containers[i].VolumeMounts = append(pod.Containers[i].VolumeMounts, vm)
		}
		for i := range pod.InitContainers {
			pod.InitContainers[i].VolumeMounts = append(pod.InitContainers[i].VolumeMounts, vm)
		}
	}
}

// mountedVolume returns the volume mounted at p in the main container, or nil.
func mountedVolume(pod *corev1.PodSpec, p string) *corev1.Volume {
	for _, m := range pod.Containers[0].VolumeMounts {
		if path.Clean(m.MountPath) != p {
			continue
		}
		for i := range pod.Volumes {
			if pod.Volumes[i].Name == m.Name {
				return &pod.Volumes[i]
			}
		}
	}
	return nil
}

// uniqueVolumeName appends -2, -3, ... when two paths sanitize to the same
// name (e.g. /a-b and /a/b).
func uniqueVolumeName(volumes []corev1.Volume, base string) string {
	if base == "" || !codesource.HasVolume(volumes, base) {
		return base
	}
	for i := 2; ; i++ {
		suffix := "-" + strconv.Itoa(i)
		name := base
		if len(name)+len(suffix) > 63 {
			name = name[:63-len(suffix)]
		}
		if name += suffix; !codesource.HasVolume(volumes, name) {
			return name
		}
	}
}
