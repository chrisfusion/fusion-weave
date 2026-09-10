// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package backup

import (
	"context"
	"fmt"
	"io"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

// GVKs for the 4 backed-up kinds. client.List zeroes TypeMeta on every returned
// item (same gotcha as r.Get() — see project CLAUDE.md), so these are used to set
// apiVersion/kind explicitly before marshaling each object to YAML.
var (
	weaveJobTemplateGVK = schema.GroupVersionKind{
		Group: "weave.fusion-platform.io", Version: "v1alpha1", Kind: "WeaveJobTemplate",
	}
	weaveServiceTemplateGVK = schema.GroupVersionKind{
		Group: "weave.fusion-platform.io", Version: "v1alpha1", Kind: "WeaveServiceTemplate",
	}
	weaveChainGVK = schema.GroupVersionKind{
		Group: "weave.fusion-platform.io", Version: "v1alpha1", Kind: "WeaveChain",
	}
	weaveTriggerGVK = schema.GroupVersionKind{
		Group: "weave.fusion-platform.io", Version: "v1alpha1", Kind: "WeaveTrigger",
	}
	configMapGVK = schema.GroupVersionKind{
		Group: "", Version: "v1", Kind: "ConfigMap",
	}
)

// stripObjectMeta clears server-managed/cluster-specific fields in place so the
// object is safe to Create() into a fresh or restored namespace. Name, Namespace,
// Labels, and Annotations survive — everything else here is either meaningless
// outside the cluster that produced it (UID, ResourceVersion) or actively harmful
// to carry across a restore (ManagedFields, SelfLink).
func stripObjectMeta(meta *metav1.ObjectMeta) {
	meta.ResourceVersion = ""
	meta.UID = ""
	meta.Generation = 0
	meta.CreationTimestamp = metav1.Time{}
	meta.ManagedFields = nil
	meta.SelfLink = ""
}

// writeDoc marshals obj to YAML and writes it to w, prefixed with a "---\n"
// separator unless it's the first document in the stream.
func writeDoc(w io.Writer, obj interface{}, first bool) error {
	if !first {
		if _, err := io.WriteString(w, "---\n"); err != nil {
			return err
		}
	}
	b, err := yaml.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshal object: %w", err)
	}
	_, err = w.Write(b)
	return err
}

// DumpObjects lists WeaveJobTemplate, WeaveServiceTemplate, WeaveChain, and
// WeaveTrigger objects in namespace and writes each as a "---"-separated YAML
// document to w, in that order — templates before the chain that references
// them, chain before the trigger that references it. WeaveRun is never included:
// it's transient execution state, not something a restore should recreate.
//
// Every BatchCron-type WeaveTrigger's referenced jobs ConfigMap
// (spec.batchCron.jobsConfigMapRef) is also captured, written last, deduplicated
// by name. Without it, restoring a BatchCron trigger's CRD alone recreates an
// object whose syncBatchCronSource immediately and permanently fails every
// reconcile with "fetch batch jobs ConfigMap ... not found", since nothing else
// ever recreates that ConfigMap.
//
// Every object has .Status zeroed and stripObjectMeta applied before marshaling,
// so the resulting stream is safe to feed straight into RestoreObjects.
//
// Returns the total number of objects written.
func DumpObjects(ctx context.Context, c client.Client, namespace string, w io.Writer) (int, error) {
	count := 0
	first := true

	var jobTemplates weavev1alpha1.WeaveJobTemplateList
	if err := c.List(ctx, &jobTemplates, client.InNamespace(namespace)); err != nil {
		return count, fmt.Errorf("list weavejobtemplates: %w", err)
	}
	for i := range jobTemplates.Items {
		item := &jobTemplates.Items[i]
		item.TypeMeta = metav1.TypeMeta{APIVersion: weaveJobTemplateGVK.GroupVersion().String(), Kind: weaveJobTemplateGVK.Kind}
		item.Status = weavev1alpha1.WeaveJobTemplateStatus{}
		stripObjectMeta(&item.ObjectMeta)
		if err := writeDoc(w, item, first); err != nil {
			return count, fmt.Errorf("write weavejobtemplate %s: %w", item.Name, err)
		}
		first = false
		count++
	}

	var serviceTemplates weavev1alpha1.WeaveServiceTemplateList
	if err := c.List(ctx, &serviceTemplates, client.InNamespace(namespace)); err != nil {
		return count, fmt.Errorf("list weaveservicetemplates: %w", err)
	}
	for i := range serviceTemplates.Items {
		item := &serviceTemplates.Items[i]
		item.TypeMeta = metav1.TypeMeta{APIVersion: weaveServiceTemplateGVK.GroupVersion().String(), Kind: weaveServiceTemplateGVK.Kind}
		item.Status = weavev1alpha1.WeaveServiceTemplateStatus{}
		stripObjectMeta(&item.ObjectMeta)
		if err := writeDoc(w, item, first); err != nil {
			return count, fmt.Errorf("write weaveservicetemplate %s: %w", item.Name, err)
		}
		first = false
		count++
	}

	var chains weavev1alpha1.WeaveChainList
	if err := c.List(ctx, &chains, client.InNamespace(namespace)); err != nil {
		return count, fmt.Errorf("list weavechains: %w", err)
	}
	for i := range chains.Items {
		item := &chains.Items[i]
		item.TypeMeta = metav1.TypeMeta{APIVersion: weaveChainGVK.GroupVersion().String(), Kind: weaveChainGVK.Kind}
		item.Status = weavev1alpha1.WeaveChainStatus{}
		stripObjectMeta(&item.ObjectMeta)
		if err := writeDoc(w, item, first); err != nil {
			return count, fmt.Errorf("write weavechain %s: %w", item.Name, err)
		}
		first = false
		count++
	}

	var triggers weavev1alpha1.WeaveTriggerList
	if err := c.List(ctx, &triggers, client.InNamespace(namespace)); err != nil {
		return count, fmt.Errorf("list weavetriggers: %w", err)
	}
	batchCronConfigMaps := map[string]bool{} // name -> already queued for dump
	for i := range triggers.Items {
		item := &triggers.Items[i]
		item.TypeMeta = metav1.TypeMeta{APIVersion: weaveTriggerGVK.GroupVersion().String(), Kind: weaveTriggerGVK.Kind}
		item.Status = weavev1alpha1.WeaveTriggerStatus{}
		stripObjectMeta(&item.ObjectMeta)
		if err := writeDoc(w, item, first); err != nil {
			return count, fmt.Errorf("write weavetrigger %s: %w", item.Name, err)
		}
		first = false
		count++

		if item.Spec.Type == weavev1alpha1.TriggerBatchCron && item.Spec.BatchCron != nil {
			batchCronConfigMaps[item.Spec.BatchCron.JobsConfigMapRef.Name] = true
		}
	}

	// Sorted for deterministic output — map iteration order is otherwise random.
	names := make([]string, 0, len(batchCronConfigMaps))
	for name := range batchCronConfigMaps {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var cm corev1.ConfigMap
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cm); err != nil {
			return count, fmt.Errorf("get batch jobs configmap %s: %w", name, err)
		}
		cm.TypeMeta = metav1.TypeMeta{APIVersion: configMapGVK.GroupVersion().String(), Kind: configMapGVK.Kind}
		stripObjectMeta(&cm.ObjectMeta)
		if err := writeDoc(w, &cm, first); err != nil {
			return count, fmt.Errorf("write configmap %s: %w", cm.Name, err)
		}
		first = false
		count++
	}

	return count, nil
}
