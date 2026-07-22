// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package codesource holds helpers shared by deploybuilder and jobbuilder for
// injecting a fusion-index code-loader init container into a pod spec.
package codesource

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"fusion-platform.io/fusion-weave/internal/indexclient"
)

// EnvVars returns the standard WEAVE_* env vars injected into every
// codeSource container, plus any runner.args from metadata as plain env vars.
// It is the single source of truth for what the runner sees at startup.
func EnvVars(artifactName, tag, version, namespace, mountPath string, meta *indexclient.AppMetadata) []corev1.EnvVar {
	vars := []corev1.EnvVar{
		{Name: "WEAVE_ARTIFACT", Value: artifactName},
		{Name: "WEAVE_TAG", Value: tag},
		{Name: "WEAVE_VERSION", Value: version},
		{Name: "WEAVE_NAMESPACE", Value: namespace},
		{Name: "WEAVE_MOUNT_PATH", Value: mountPath},
	}
	if meta != nil {
		if meta.Runner.Port > 0 {
			vars = append(vars, corev1.EnvVar{Name: "WEAVE_PORT", Value: strconv.Itoa(int(meta.Runner.Port))})
		}
		if meta.Ingress.PathPrefix != "" {
			vars = append(vars, corev1.EnvVar{Name: "WEAVE_INGRESS_PATH_PREFIX", Value: meta.Ingress.PathPrefix})
		}
		if meta.Runner.Type != "" {
			vars = append(vars, corev1.EnvVar{Name: "WEAVE_RUNNER_TYPE", Value: meta.Runner.Type})
		}
		if meta.Runner.BuilderImage != "" {
			vars = append(vars, corev1.EnvVar{Name: "WEAVE_BUILDER_IMAGE", Value: meta.Runner.BuilderImage})
		}
		if meta.Maintainer != "" {
			vars = append(vars, corev1.EnvVar{Name: "WEAVE_MAINTAINER", Value: meta.Maintainer})
		}
		for k, v := range meta.Runner.Args {
			vars = append(vars, corev1.EnvVar{Name: k, Value: v})
		}
	}
	return vars
}

// WritableVolumeName converts a mount path to a valid Kubernetes volume name
// (DNS label: lowercase alphanumeric and hyphens, max 63 chars).
// e.g. /home/nonroot → weave-w-home-nonroot
// Returns "" for paths that produce an empty slug (e.g. "/").
func WritableVolumeName(path string) string {
	clean := strings.ToLower(strings.TrimLeft(path, "/"))
	var b strings.Builder
	for _, r := range clean {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return ""
	}
	const prefix = "weave-w-"
	const maxSlug = 63 - len(prefix)
	if len(slug) > maxSlug {
		slug = strings.TrimRight(slug[:maxSlug], "-")
	}
	return prefix + slug
}

// TruncateK8sName shortens name to at most maxLen bytes, replacing any
// truncated portion with a short deterministic hash so distinct long names
// don't collide after truncation. No-op when name already fits.
//
// batch/v1 Job and Service names must be <=63 bytes (not the generic 253-byte
// object-name limit) because Kubernetes auto-derives a label from the name
// (Job: "job-name"; Service: selector/endpoint labels) and label values are
// capped at 63 bytes — a longer name is rejected by the API server.
func TruncateK8sName(name string, maxLen int) string {
	if len(name) <= maxLen {
		return name
	}
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(name))
	hash := fmt.Sprintf("%08x", sum.Sum32())
	keep := maxLen - len(hash) - 1
	if keep < 0 {
		keep = 0
	}
	return strings.TrimRight(name[:keep], "-") + "-" + hash
}

// HasVolume reports whether a volume with the given name is already in the slice.
func HasVolume(volumes []corev1.Volume, name string) bool {
	for _, v := range volumes {
		if v.Name == name {
			return true
		}
	}
	return false
}
