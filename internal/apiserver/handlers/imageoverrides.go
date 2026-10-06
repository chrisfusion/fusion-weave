// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import "net/http"

// ImageOverrideOptions reports the image prefixes accepted for
// WeaveRun/WeaveTrigger imageOverrides. An empty list means overrides are
// disabled on this cluster.
type ImageOverrideOptions struct {
	AllowedPrefixes []string `json:"allowedPrefixes"`
}

// NewImageOverrideOptionsHandler echoes the static ALLOWED_IMAGE_PREFIXES list
// so a GUI can hint and pre-validate; no cluster lookups.
func NewImageOverrideOptionsHandler(prefixes []string) http.HandlerFunc {
	opts := ImageOverrideOptions{AllowedPrefixes: append([]string{}, prefixes...)}
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, opts)
	}
}
