// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// safeMapFunc wraps a handler.EnqueueRequestsFromMapFunc callback with panic
// recovery. These callbacks run once per relevant object in the informer's own
// goroutine (client-go's shared informer processor), not inside a Reconcile call —
// so they are NOT covered by controller-runtime's per-Reconcile panic recovery. An
// unrecovered panic there crashes the whole operator process, and because the
// callback runs for every matching object across the cluster, a single malformed
// or legacy object (e.g. a WeaveChain from an older CRD schema) could take down
// reconciliation for every other chain and run. Recovering here degrades that to
// "this one enqueue was skipped, logged, and reconciliation continues."
func safeMapFunc(label string, fn func(ctx context.Context, obj client.Object) []reconcile.Request) func(ctx context.Context, obj client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) (reqs []reconcile.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.FromContext(ctx).Error(fmt.Errorf("%v", rec), "panic recovered in enqueue map func",
					"handler", label, "object", client.ObjectKeyFromObject(obj))
				reqs = nil
			}
		}()
		return fn(ctx, obj)
	}
}
