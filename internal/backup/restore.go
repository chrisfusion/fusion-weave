// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package backup

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

// RestoreResult reports per-run created/skipped counts and any non-fatal
// per-object errors encountered while restoring a backup.
type RestoreResult struct {
	Created int
	Skipped int
	Errors  []error
}

// HasExistingObjects reports whether namespace already contains at least one
// WeaveJobTemplate, WeaveServiceTemplate, WeaveChain, or WeaveTrigger — used to
// gate RESTORE_FORCE. Unlike a database restore there's no "schema not installed
// yet" edge case to distinguish: CRDs are assumed already installed before restore
// ever runs, so an empty List is unambiguously "safe to restore into".
func HasExistingObjects(ctx context.Context, c client.Client, namespace string) (bool, error) {
	var jobTemplates weavev1alpha1.WeaveJobTemplateList
	if err := c.List(ctx, &jobTemplates, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("list weavejobtemplates: %w", err)
	}
	if len(jobTemplates.Items) > 0 {
		return true, nil
	}

	var serviceTemplates weavev1alpha1.WeaveServiceTemplateList
	if err := c.List(ctx, &serviceTemplates, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("list weaveservicetemplates: %w", err)
	}
	if len(serviceTemplates.Items) > 0 {
		return true, nil
	}

	var chains weavev1alpha1.WeaveChainList
	if err := c.List(ctx, &chains, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("list weavechains: %w", err)
	}
	if len(chains.Items) > 0 {
		return true, nil
	}

	var triggers weavev1alpha1.WeaveTriggerList
	if err := c.List(ctx, &triggers, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("list weavetriggers: %w", err)
	}
	return len(triggers.Items) > 0, nil
}

// kindMeta is used to peek a document's "kind" field before deciding which
// concrete typed struct to unmarshal it into.
type kindMeta struct {
	Kind string `json:"kind"`
}

// RestoreObjects reads a "---"-separated multi-document YAML stream (as produced
// by DumpObjects), dispatches each document by its "kind" field into the matching
// typed struct, forces its Namespace to namespace — never trust the namespace
// embedded in the backup, since restore may target a namespace re-created after a
// hard loss — and Create()s it. An individual AlreadyExists is logged as Skipped,
// not fatal; any other Create error is collected and restore continues with the
// next document.
func RestoreObjects(ctx context.Context, c client.Client, r io.Reader, namespace string) (RestoreResult, error) {
	var result RestoreResult

	reader := k8syaml.NewYAMLReader(bufio.NewReader(r))
	for {
		raw, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, fmt.Errorf("read yaml document: %w", err)
		}
		// The reader's very first document includes a leading "---" marker line
		// verbatim when the stream opens with a separator (e.g. blank documents
		// at the start of the stream) — strip it before checking for blankness.
		trimmed := bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(raw), []byte("---")))
		if len(trimmed) == 0 {
			continue
		}

		var km kindMeta
		if err := yaml.Unmarshal(raw, &km); err != nil {
			return result, fmt.Errorf("parse document kind: %w", err)
		}
		if km.Kind == "" {
			continue
		}

		var obj client.Object
		switch km.Kind {
		case "WeaveJobTemplate":
			o := &weavev1alpha1.WeaveJobTemplate{}
			if err := yaml.Unmarshal(raw, o); err != nil {
				return result, fmt.Errorf("unmarshal WeaveJobTemplate: %w", err)
			}
			obj = o
		case "WeaveServiceTemplate":
			o := &weavev1alpha1.WeaveServiceTemplate{}
			if err := yaml.Unmarshal(raw, o); err != nil {
				return result, fmt.Errorf("unmarshal WeaveServiceTemplate: %w", err)
			}
			obj = o
		case "WeaveChain":
			o := &weavev1alpha1.WeaveChain{}
			if err := yaml.Unmarshal(raw, o); err != nil {
				return result, fmt.Errorf("unmarshal WeaveChain: %w", err)
			}
			obj = o
		case "WeaveTrigger":
			o := &weavev1alpha1.WeaveTrigger{}
			if err := yaml.Unmarshal(raw, o); err != nil {
				return result, fmt.Errorf("unmarshal WeaveTrigger: %w", err)
			}
			obj = o
		default:
			return result, fmt.Errorf("unrecognized kind %q in backup document", km.Kind)
		}

		obj.SetNamespace(namespace)

		if err := c.Create(ctx, obj); err != nil {
			if apierrors.IsAlreadyExists(err) {
				result.Skipped++
				continue
			}
			result.Errors = append(result.Errors, fmt.Errorf("create %s %q: %w", km.Kind, obj.GetName(), err))
			continue
		}
		result.Created++
	}

	return result, nil
}
