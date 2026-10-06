// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package imagepolicy validates container images supplied through WeaveRun
// image overrides: an explicit immutable tag or digest, and an allowed prefix.
package imagepolicy

import (
	"fmt"
	"strings"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

// ParsePrefixes splits an ALLOWED_IMAGE_PREFIXES value on commas, trimming
// whitespace and dropping empty entries. Unlike the colon-separated path
// allowlists, commas are used because registry prefixes contain ":" (host:port).
func ParsePrefixes(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Validate checks one override image. It must carry a digest or an explicit tag
// other than "latest", and start with one of allowedPrefixes. An empty
// allowedPrefixes disables overrides entirely.
func Validate(image string, allowedPrefixes []string) error {
	if len(allowedPrefixes) == 0 {
		return fmt.Errorf("image overrides are disabled (ALLOWED_IMAGE_PREFIXES is not configured)")
	}
	if image == "" || strings.ContainsAny(image, " \t\n") {
		return fmt.Errorf("image %q is empty or contains whitespace", image)
	}
	if !hasImmutableRef(image) {
		return fmt.Errorf("image %q must have an explicit tag (not \":latest\") or a digest", image)
	}
	for _, p := range allowedPrefixes {
		if strings.HasPrefix(image, p) {
			return nil
		}
	}
	return fmt.Errorf("image %q does not match any allowed prefix", image)
}

// ValidateList checks the chain-independent rules for a list of overrides
// (shared by the run and trigger API handlers): unique step names and every
// image passing Validate.
func ValidateList(overrides []weavev1alpha1.WeaveRunImageOverride, allowedPrefixes []string) error {
	seen := map[string]bool{}
	for _, o := range overrides {
		if seen[o.StepName] {
			return fmt.Errorf("duplicate image override for step %q", o.StepName)
		}
		seen[o.StepName] = true
		if err := Validate(o.Image, allowedPrefixes); err != nil {
			return fmt.Errorf("step %q: %w", o.StepName, err)
		}
	}
	return nil
}

func hasImmutableRef(image string) bool {
	if strings.Contains(image, "@sha256:") {
		return true
	}
	// The tag is whatever follows the last ":" after the last "/" (a ":" before
	// that belongs to a registry host:port).
	name := image[strings.LastIndex(image, "/")+1:]
	i := strings.LastIndex(name, ":")
	if i < 0 {
		return false
	}
	tag := name[i+1:]
	return tag != "" && tag != "latest"
}

// ValidateOverrides validates every entry of a run's image overrides and checks
// step names against the chain: each must exist and appear only once.
func ValidateOverrides(overrides []weavev1alpha1.WeaveRunImageOverride, stepKinds map[string]weavev1alpha1.WeaveStepKind, runOwned map[string]bool, allowedPrefixes []string) error {
	seen := map[string]bool{}
	for _, o := range overrides {
		if seen[o.StepName] {
			return fmt.Errorf("duplicate image override for step %q", o.StepName)
		}
		seen[o.StepName] = true
		kind, ok := stepKinds[o.StepName]
		if !ok {
			return fmt.Errorf("image override references unknown step %q", o.StepName)
		}
		if kind == weavev1alpha1.StepKindDeploy && !runOwned[o.StepName] {
			return fmt.Errorf("image override for deploy step %q requires a stepOverrides entry (run-owned Deployment); chain-owned Deployments are shared across runs", o.StepName)
		}
		if err := Validate(o.Image, allowedPrefixes); err != nil {
			return fmt.Errorf("step %q: %w", o.StepName, err)
		}
	}
	return nil
}
