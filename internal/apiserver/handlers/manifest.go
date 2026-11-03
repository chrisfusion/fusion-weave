// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"net/http"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/yaml"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

// manifestDropMetadata are metadata fields describing cluster state rather than desired state.
// namespace is dropped too: a gitops repo is applied into the namespace its customer is pinned to.
var manifestDropMetadata = []string{
	"namespace", "uid", "resourceVersion", "generation", "creationTimestamp",
	"deletionTimestamp", "deletionGracePeriodSeconds", "managedFields",
	"ownerReferences", "finalizers", "selfLink",
}

// manifestDropAnnotations are cluster-side noise or one-shot triggers that would re-fire on every
// Flux apply.
var manifestDropAnnotations = []string{
	"kubectl.kubernetes.io/last-applied-configuration",
	weavev1alpha1.WeaveAppFireAnnotation,
}

// manifestDropLabelPrefixes: Flux stamps its own labels, and the managed-by classification
// (manual, wizard) no longer holds once the manifest lives in git.
var manifestDropLabelPrefixes = []string{"kustomize.toolkit.fluxcd.io/", labelManagedBy}

// writeManifest fetches the named object into obj and writes it as YAML without status and
// cluster-managed metadata, ready to commit to a gitops repo. ?download=true adds a
// Content-Disposition of <kind>-<name>.yaml so downloads of different kinds never collide.
func (b *base) writeManifest(w http.ResponseWriter, r *http.Request, obj client.Object) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	if err := b.client.Get(r.Context(), types.NamespacedName{Namespace: b.namespace, Name: name}, obj); err != nil {
		handleGetErr(w, r, err)
		return
	}
	// client.Get leaves TypeMeta empty for typed objects.
	gvk, err := apiutil.GVKForObject(obj, b.client.Scheme())
	if err != nil {
		internalError(w, r, err, "name", name)
		return
	}
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		internalError(w, r, err, "kind", gvk.Kind, "name", name)
		return
	}
	out, err := yaml.Marshal(cleanManifest(u, gvk.GroupVersion().String(), gvk.Kind))
	if err != nil {
		internalError(w, r, err, "kind", gvk.Kind, "name", name)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	if r.URL.Query().Get("download") == "true" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ToLower(gvk.Kind)+"-"+name+`.yaml"`)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// cleanManifest reduces an unstructured object to apiVersion, kind, metadata (name, labels,
// annotations) and spec.
func cleanManifest(u map[string]any, apiVersion, kind string) map[string]any {
	u["apiVersion"] = apiVersion
	u["kind"] = kind
	delete(u, "status")
	meta, _ := u["metadata"].(map[string]any)
	if meta == nil {
		return u
	}
	for _, f := range manifestDropMetadata {
		delete(meta, f)
	}
	if ann, _ := meta["annotations"].(map[string]any); ann != nil {
		for _, k := range manifestDropAnnotations {
			delete(ann, k)
		}
	}
	if lbl, _ := meta["labels"].(map[string]any); lbl != nil {
		for k := range lbl {
			for _, p := range manifestDropLabelPrefixes {
				if strings.HasPrefix(k, p) {
					delete(lbl, k)
				}
			}
		}
	}
	for _, f := range []string{"labels", "annotations"} {
		if m, _ := meta[f].(map[string]any); len(m) == 0 {
			delete(meta, f)
		}
	}
	return u
}
