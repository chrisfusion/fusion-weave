// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/trigger"
)

const (
	labelTrigger = "fusion-platform.io/trigger"
	labelChain   = "fusion-platform.io/chain"

	annotationFire = "fusion-platform.io/fire"
)

// WeaveTriggerReconciler manages activation sources and creates WeaveRun objects.
type WeaveTriggerReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	CronScheduler *trigger.CronScheduler
	WebhookServer *trigger.WebhookServer
	// FireCh receives fire requests from webhook callbacks.
	FireCh <-chan trigger.FireRequest

	// wakeupCh is used by cron and webhook callbacks to enqueue a reconcile.
	wakeupCh chan event.GenericEvent

	// pendingFires holds one-shot fire requests received from cron/webhook.
	// Key: "<namespace>/<name>", value: parameter overrides.
	pendingFiresMu sync.Mutex
	pendingFires   map[string][]corev1.EnvVar
}

// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=fluxtriggers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=fluxtriggers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=fluxruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func NewWeaveTriggerReconciler(c client.Client, scheme *runtime.Scheme,
	cron *trigger.CronScheduler, webhook *trigger.WebhookServer,
	fireCh <-chan trigger.FireRequest,
) *WeaveTriggerReconciler {
	r := &WeaveTriggerReconciler{
		Client:        c,
		Scheme:        scheme,
		CronScheduler: cron,
		WebhookServer: webhook,
		FireCh:        fireCh,
		wakeupCh:      make(chan event.GenericEvent, 64),
		pendingFires:  make(map[string][]corev1.EnvVar),
	}
	go r.drainFireChannel()
	return r
}

// drainFireChannel reads from FireCh, stores the pending fire, and wakes the reconciler.
func (r *WeaveTriggerReconciler) drainFireChannel() {
	for req := range r.FireCh {
		key := req.TriggerNamespace + "/" + req.TriggerName
		r.pendingFiresMu.Lock()
		r.pendingFires[key] = req.ParameterOverrides
		r.pendingFiresMu.Unlock()
		// Send a wakeup event so the reconciler runs without waiting for a k8s object change.
		r.sendWakeup(req.TriggerNamespace, req.TriggerName)
	}
}

// sendWakeup enqueues a GenericEvent for the given trigger so the reconciler runs immediately.
func (r *WeaveTriggerReconciler) sendWakeup(namespace, name string) {
	obj := &weavev1alpha1.WeaveTrigger{}
	obj.Namespace = namespace
	obj.Name = name
	select {
	case r.wakeupCh <- event.GenericEvent{Object: obj}:
	default: // channel full — reconciler is already queued
	}
}

func (r *WeaveTriggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var ft weavev1alpha1.WeaveTrigger
	if err := r.Get(ctx, req.NamespacedName, &ft); err != nil {
		if errors.IsNotFound(err) {
			r.CronScheduler.Remove(req.String())
			if r.WebhookServer != nil {
				r.WebhookServer.Unregister(req.String())
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Verify the referenced chain is valid.
	var chain weavev1alpha1.WeaveChain
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: ft.Namespace, Name: ft.Spec.ChainRef.Name,
	}, &chain); err != nil {
		return r.setInactive(ctx, &ft, fmt.Sprintf("chain %q not found", ft.Spec.ChainRef.Name))
	}
	if !chain.Status.Valid {
		return r.setInactive(ctx, &ft, fmt.Sprintf("chain %q is not valid", ft.Spec.ChainRef.Name))
	}

	// Register activation sources.
	if err := r.syncActivationSources(ctx, &ft, req.String()); err != nil {
		return ctrl.Result{}, err
	}

	// Check for a pending fire request (from cron callback or webhook).
	key := req.NamespacedName.String()
	overrides, hasFire := r.consumePendingFire(key)

	// Check for on-demand fire annotation.
	if !hasFire && ft.Annotations[annotationFire] == "true" {
		hasFire = true
		// Remove the annotation so it does not fire again.
		patch := client.MergeFrom(ft.DeepCopy())
		delete(ft.Annotations, annotationFire)
		if err := r.Patch(ctx, &ft, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("remove fire annotation: %w", err)
		}
	}

	if hasFire {
		result, err := r.maybeCreateRun(ctx, &ft, &chain, overrides)
		if err != nil || result.RequeueAfter > 0 {
			return result, err
		}
	}

	// Mark active.
	patch := client.MergeFrom(ft.DeepCopy())
	ft.Status.Active = true
	if err := r.Status().Patch(ctx, &ft, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch status: %w", err)
	}

	logger.Info("WeaveTrigger reconciled", "type", ft.Spec.Type)
	return ctrl.Result{}, nil
}

func (r *WeaveTriggerReconciler) syncActivationSources(ctx context.Context, ft *weavev1alpha1.WeaveTrigger, key string) error {
	switch ft.Spec.Type {
	case weavev1alpha1.TriggerCron:
		if ft.Spec.Schedule == "" {
			return fmt.Errorf("spec.schedule is required for Cron trigger")
		}
		ns, name := ft.Namespace, ft.Name
		return r.CronScheduler.Upsert(key, ft.Spec.Schedule, func() {
			r.storePendingFire(key, nil)
			r.sendWakeup(ns, name)
		})

	case weavev1alpha1.TriggerWebhook:
		if r.WebhookServer == nil || ft.Spec.Webhook == nil {
			return nil
		}
		r.WebhookServer.Register(ft.Spec.Webhook.Path, ft.Namespace, ft.Name)
		// Inform status of the webhook URL (informational).
		patch := client.MergeFrom(ft.DeepCopy())
		ft.Status.WebhookURL = fmt.Sprintf("http://<operator-svc>%s", ft.Spec.Webhook.Path)
		return r.Status().Patch(ctx, ft, patch)
	}
	return nil
}

// maybeCreateRun enforces the ConcurrencyPolicy and creates a WeaveRun if allowed.
// chain is passed in to avoid a redundant API call (already fetched by Reconcile).
func (r *WeaveTriggerReconciler) maybeCreateRun(
	ctx context.Context,
	ft *weavev1alpha1.WeaveTrigger,
	chain *weavev1alpha1.WeaveChain,
	overrides []corev1.EnvVar,
) (ctrl.Result, error) {
	// List active or pending runs for this trigger.
	var runList weavev1alpha1.WeaveRunList
	if err := r.List(ctx, &runList,
		client.InNamespace(ft.Namespace),
		client.MatchingLabels{labelTrigger: ft.Name},
	); err != nil {
		return ctrl.Result{}, err
	}

	hasActive := false
	for _, run := range runList.Items {
		if run.Status.Phase == weavev1alpha1.RunPhasePending ||
			run.Status.Phase == weavev1alpha1.RunPhaseRunning ||
			run.Status.Phase == "" {
			hasActive = true
			break
		}
	}

	if hasActive {
		switch chain.Spec.ConcurrencyPolicy {
		case weavev1alpha1.ConcurrencyWait:
			// Re-check after 15 seconds.
			r.storePendingFire(ft.Namespace+"/"+ft.Name, overrides)
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		case weavev1alpha1.ConcurrencyForbid:
			return ctrl.Result{}, nil
		}
	}

	return ctrl.Result{}, r.createRun(ctx, ft, overrides)
}

func (r *WeaveTriggerReconciler) createRun(
	ctx context.Context,
	ft *weavev1alpha1.WeaveTrigger,
	overrides []corev1.EnvVar,
) error {
	// Merge trigger-level parameter overrides with per-call overrides.
	merged := mergeEnvVars(ft.Spec.ParameterOverrides, overrides)

	run := &weavev1alpha1.WeaveRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: ft.Name + "-",
			Namespace:    ft.Namespace,
			Labels: map[string]string{
				labelTrigger: ft.Name,
				labelChain:   ft.Spec.ChainRef.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(ft, weavev1alpha1.GroupVersion.WithKind("WeaveTrigger")),
			},
		},
		Spec: weavev1alpha1.WeaveRunSpec{
			ChainRef:           ft.Spec.ChainRef,
			TriggerRef:         &corev1.LocalObjectReference{Name: ft.Name},
			ParameterOverrides: merged,
		},
	}

	if err := r.Create(ctx, run); err != nil {
		return fmt.Errorf("create WeaveRun: %w", err)
	}

	now := metav1.Now()
	patch := client.MergeFrom(ft.DeepCopy())
	ft.Status.LastScheduleTime = &now
	ft.Status.LastRunName = run.Name
	return r.Status().Patch(ctx, ft, patch)
}

func (r *WeaveTriggerReconciler) setInactive(ctx context.Context, ft *weavev1alpha1.WeaveTrigger, msg string) (ctrl.Result, error) {
	patch := client.MergeFrom(ft.DeepCopy())
	ft.Status.Active = false
	if err := r.Status().Patch(ctx, ft, patch); err != nil {
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).Info("WeaveTrigger inactive", "reason", msg)
	return ctrl.Result{}, nil
}

func (r *WeaveTriggerReconciler) storePendingFire(key string, overrides []corev1.EnvVar) {
	r.pendingFiresMu.Lock()
	r.pendingFires[key] = overrides
	r.pendingFiresMu.Unlock()
}

func (r *WeaveTriggerReconciler) consumePendingFire(key string) ([]corev1.EnvVar, bool) {
	r.pendingFiresMu.Lock()
	defer r.pendingFiresMu.Unlock()
	overrides, ok := r.pendingFires[key]
	if ok {
		delete(r.pendingFires, key)
	}
	return overrides, ok
}

func mergeEnvVars(base, overrides []corev1.EnvVar) []corev1.EnvVar {
	seen := map[string]int{}
	result := make([]corev1.EnvVar, 0, len(base)+len(overrides))
	for _, e := range base {
		seen[e.Name] = len(result)
		result = append(result, e)
	}
	for _, e := range overrides {
		if idx, ok := seen[e.Name]; ok {
			result[idx] = e
		} else {
			result = append(result, e)
		}
	}
	return result
}

func (r *WeaveTriggerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Wake trigger reconciler when an owned WeaveRun completes.
	enqueueFromRun := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		run, ok := obj.(*weavev1alpha1.WeaveRun)
		if !ok {
			return nil
		}
		triggerName, ok := run.Labels[labelTrigger]
		if !ok {
			return nil
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Namespace: run.Namespace,
				Name:      triggerName,
			},
		}}
	})

	// source.Channel delivers GenericEvents from cron/webhook callbacks directly
	// into the reconciler queue, bypassing the need for a k8s object change.
	wakeupSource := source.Channel(r.wakeupCh, handler.EnqueueRequestsFromMapFunc(
		func(_ context.Context, obj client.Object) []reconcile.Request {
			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{
					Namespace: obj.GetNamespace(),
					Name:      obj.GetName(),
				},
			}}
		},
	))

	return ctrl.NewControllerManagedBy(mgr).
		For(&weavev1alpha1.WeaveTrigger{}).
		Watches(&weavev1alpha1.WeaveRun{}, enqueueFromRun).
		WatchesRawSource(wakeupSource).
		Complete(r)
}
