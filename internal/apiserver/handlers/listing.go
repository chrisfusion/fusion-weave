// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"errors"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/chrisfusion/fusion-shared/listquery"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// paramLabelSelector is a Kubernetes label selector applied by the API server's list call;
// every other list parameter is applied in memory by listquery.
const paramLabelSelector = "labelSelector"

// parseList reads the shared list parameters (see listquery) plus labelSelector.
// It writes 400 and returns false on a bad parameter.
func (b *base) parseList(w http.ResponseWriter, r *http.Request, spec listquery.Spec) (listquery.Params, []client.ListOption, bool) {
	q := r.URL.Query()
	p, err := listquery.Parse(q, spec, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return p, nil, false
	}
	opts := []client.ListOption{client.InNamespace(b.namespace)}
	if raw := q.Get(paramLabelSelector); raw != "" {
		sel, err := labels.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, paramLabelSelector+": "+err.Error())
			return p, nil, false
		}
		opts = append(opts, client.MatchingLabelsSelector{Selector: sel})
	}
	return p, opts, true
}

// badParam writes 400 for an endpoint-specific parameter error and reports whether it did.
func badParam(w http.ResponseWriter, errs ...error) bool {
	if err := errors.Join(errs...); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return true
	}
	return false
}

// setPage records pagination on a Kubernetes list, the way the Kubernetes API does:
// continue and remainingItemCount only while more items follow.
func setPage(meta *metav1.ListMeta, continueToken string, remaining int) {
	meta.Continue = continueToken
	meta.RemainingItemCount = nil
	if continueToken != "" {
		n := int64(remaining)
		meta.RemainingItemCount = &n
	}
}

// pagedList is the response of list endpoints that are not backed by a Kubernetes list
// type; it mirrors the Kubernetes list shape so clients page all lists the same way.
type pagedList[T any] struct {
	Metadata metav1.ListMeta `json:"metadata"`
	Items    []T             `json:"items"`
}

func newPagedList[T any](page listquery.Page[T]) pagedList[T] {
	out := pagedList[T]{Items: page.Items}
	if out.Items == nil {
		out.Items = []T{}
	}
	setPage(&out.Metadata, page.Continue, page.Remaining)
	return out
}

// objectFields returns listquery fields for a Kubernetes object type with the sort keys
// "name" and "created". timeOf sets the field of the since/until window (nil = creation
// time); sorts adds endpoint-specific sort keys.
func objectFields[T any, PT interface {
	*T
	client.Object
}](timeOf func(*T) time.Time, sorts map[string]func(a, b *T) int) listquery.Fields[T] {
	created := func(it *T) time.Time { return PT(it).GetCreationTimestamp().Time }
	if timeOf == nil {
		timeOf = created
	}
	f := listquery.Fields[T]{
		Name:   func(it *T) string { return PT(it).GetName() },
		Labels: func(it *T) map[string]string { return PT(it).GetLabels() },
		Time:   timeOf,
		Sort: map[string]func(a, b *T) int{
			"name":    func(a, b *T) int { return strings.Compare(PT(a).GetName(), PT(b).GetName()) },
			"created": func(a, b *T) int { return listquery.CompareTime(created(a), created(b)) },
		},
	}
	maps.Copy(f.Sort, sorts)
	return f
}

// timeOf returns t's time, or the zero time when t is nil.
func timeOf(t *metav1.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.Time
}

// boolMatch reports whether v satisfies an optional boolean filter (nil = no filter).
func boolMatch(want *bool, v bool) bool { return want == nil || *want == v }

// enumStrings converts a typed string enum to plain strings for listquery.OneOf.
func enumStrings[S ~string](vals ...S) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = string(v)
	}
	return out
}
