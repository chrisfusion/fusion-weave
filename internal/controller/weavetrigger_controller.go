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
	labelBatchJobID   = "fusion-platform.io/batch-job-id"
	labelBatchTrigger = "fusion-platform.io/batch-trigger"
)

const (
	labelTrigger = "fusion-platform.io/trigger"
	labelChain   = "fusion-platform.io/chain"

	annotationFire = "fusion-platform.io/fire"
)

// WeaveTriggerReconciler manages activation sources and creates WeaveRun objects.
type WeaveTriggerReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	CronScheduler      *trigger.CronScheduler
	BatchCronScheduler *trigger.BatchCronScheduler
	KafkaConsumer      *trigger.KafkaConsumer
	WebhookServer      *trigger.WebhookServer
	// FireCh receives fire requests from webhook callbacks.
	FireCh <-chan trigger.FireRequest
	// BatchFireCh receives per-job fire requests from the BatchCronScheduler.
	BatchFireCh <-chan trigger.BatchFireRequest
	// KafkaFireCh receives fire requests from the KafkaConsumer.
	KafkaFireCh <-chan trigger.KafkaFireRequest

	// wakeupCh is used by cron and webhook callbacks to enqueue a reconcile.
	wakeupCh chan event.GenericEvent

	// pendingFires holds one-shot fire requests received from cron/webhook.
	// Key: "<namespace>/<name>", value: parameter overrides.
	pendingFiresMu sync.Mutex
	pendingFires   map[string][]corev1.EnvVar

	// pendingBatchFires holds queued per-job fires from the BatchCronScheduler.
	// Key: "<namespace>/<name>", value: ordered list of fire requests.
	pendingBatchFiresMu sync.Mutex
	pendingBatchFires   map[string][]trigger.BatchFireRequest

	// pendingKafkaFires holds queued Kafka-sourced fires.
	// Key: "<namespace>/<name>", value: ordered list of fire requests.
	pendingKafkaFiresMu sync.Mutex
	pendingKafkaFires   map[string][]trigger.KafkaFireRequest
}

// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=fluxtriggers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=fluxtriggers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=fluxruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func NewWeaveTriggerReconciler(
	c client.Client,
	scheme *runtime.Scheme,
	cron *trigger.CronScheduler,
	batchCron *trigger.BatchCronScheduler,
	kafkaConsumer *trigger.KafkaConsumer,
	webhook *trigger.WebhookServer,
	fireCh <-chan trigger.FireRequest,
	batchFireCh <-chan trigger.BatchFireRequest,
	kafkaFireCh <-chan trigger.KafkaFireRequest,
) *WeaveTriggerReconciler {
	r := &WeaveTriggerReconciler{
		Client:             c,
		Scheme:             scheme,
		CronScheduler:      cron,
		BatchCronScheduler: batchCron,
		KafkaConsumer:      kafkaConsumer,
		WebhookServer:      webhook,
		FireCh:             fireCh,
		BatchFireCh:        batchFireCh,
		KafkaFireCh:        kafkaFireCh,
		wakeupCh:           make(chan event.GenericEvent, 64),
		pendingFires:       make(map[string][]corev1.EnvVar),
		pendingBatchFires:  make(map[string][]trigger.BatchFireRequest),
		pendingKafkaFires:  make(map[string][]trigger.KafkaFireRequest),
	}
	go r.drainFireChannel()
	go r.drainBatchFireChannel()
	go r.drainKafkaFireChannel()
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

// drainBatchFireChannel reads from BatchFireCh, queues the fire, and wakes the reconciler.
func (r *WeaveTriggerReconciler) drainBatchFireChannel() {
	for req := range r.BatchFireCh {
		key := req.TriggerNamespace + "/" + req.TriggerName
		r.pendingBatchFiresMu.Lock()
		r.pendingBatchFires[key] = append(r.pendingBatchFires[key], req)
		r.pendingBatchFiresMu.Unlock()
		r.sendWakeup(req.TriggerNamespace, req.TriggerName)
	}
}

// drainKafkaFireChannel reads from KafkaFireCh, queues the fire, and wakes the reconciler.
func (r *WeaveTriggerReconciler) drainKafkaFireChannel() {
	for req := range r.KafkaFireCh {
		key := req.TriggerNamespace + "/" + req.TriggerName
		r.pendingKafkaFiresMu.Lock()
		r.pendingKafkaFires[key] = append(r.pendingKafkaFires[key], req)
		r.pendingKafkaFiresMu.Unlock()
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
			r.BatchCronScheduler.Remove(req.String())
			r.KafkaConsumer.Remove(req.String())
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

	// Process queued batch-cron fires (one WeaveRun per job per fire).
	batchFires, hasBatchFires := r.consumePendingBatchFires(key)
	if hasBatchFires {
		for _, fire := range batchFires {
			if err := r.maybeCreateBatchRun(ctx, &ft, &chain, fire); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// Process queued Kafka fires (one WeaveRun per message, subject to throttle).
	kafkaFires, hasKafkaFires := r.consumePendingKafkaFires(key)
	if hasKafkaFires {
		for _, fire := range kafkaFires {
			if err := r.maybeCreateKafkaRun(ctx, &ft, &chain, fire); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// Mark active.
	patch := client.MergeFrom(ft.DeepCopy())
	ft.Status.Active = true
	if err := r.Status().Patch(ctx, &ft, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch status: %w", err)
	}

	logger.Info("WeaveTrigger reconciled", "trigger", ft.Name, "chain", ft.Spec.ChainRef.Name, "type", ft.Spec.Type)
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

	case weavev1alpha1.TriggerBatchCron:
		return r.syncBatchCronSource(ctx, ft, key)

	case weavev1alpha1.TriggerKafka:
		return r.syncKafkaSource(ctx, ft, key)
	}
	return nil
}

func (r *WeaveTriggerReconciler) syncBatchCronSource(ctx context.Context, ft *weavev1alpha1.WeaveTrigger, key string) error {
	logger := log.FromContext(ctx)

	if ft.Spec.BatchCron == nil {
		return fmt.Errorf("spec.batchCron is required for BatchCron trigger")
	}

	// Fetch the ConfigMap that holds the job list YAML.
	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: ft.Namespace,
		Name:      ft.Spec.BatchCron.JobsConfigMapRef.Name,
	}, &cm); err != nil {
		return fmt.Errorf("fetch batch jobs ConfigMap %q: %w", ft.Spec.BatchCron.JobsConfigMapRef.Name, err)
	}

	// Ensure the ConfigMap carries the watch label so future edits re-trigger reconciliation.
	// This handles manually-created ConfigMaps (kubectl apply, GitOps) that lack the label.
	if cm.Labels[labelBatchTrigger] != ft.Name {
		patch := client.MergeFrom(cm.DeepCopy())
		if cm.Labels == nil {
			cm.Labels = make(map[string]string)
		}
		cm.Labels[labelBatchTrigger] = ft.Name
		if err := r.Patch(ctx, &cm, patch); err != nil {
			return fmt.Errorf("label batch ConfigMap: %w", err)
		}
	}

	yamlContent := cm.Data["jobs.yaml"]
	jobs, errs := trigger.ParseBatchJobs(yamlContent)

	logger.Info("batch cron jobs loaded", "trigger", ft.Name,
		"total", len(jobs), "errors", len(errs))

	// Update status with job counts.
	patch := client.MergeFrom(ft.DeepCopy())
	ft.Status.Active = true
	ft.Status.BatchJobCount = len(jobs)
	ft.Status.BatchJobErrors = len(errs)
	if err := r.Status().Patch(ctx, ft, patch); err != nil {
		return fmt.Errorf("patch batch status: %w", err)
	}

	if ft.Spec.Paused {
		r.BatchCronScheduler.Remove(key)
		return nil
	}

	r.BatchCronScheduler.Upsert(key, ft.Namespace, ft.Name, jobs)
	return nil
}

func (r *WeaveTriggerReconciler) syncKafkaSource(ctx context.Context, ft *weavev1alpha1.WeaveTrigger, key string) error {
	if ft.Spec.Kafka == nil {
		return fmt.Errorf("spec.kafka is required for Kafka trigger")
	}

	if ft.Spec.Paused {
		r.KafkaConsumer.Remove(key)
		return nil
	}

	cfg := trigger.KafkaRunnerConfig{
		Brokers:       ft.Spec.Kafka.Brokers,
		Topic:         ft.Spec.Kafka.Topic,
		ConsumerGroup: ft.Spec.Kafka.ConsumerGroup,
		EventFilter:   ft.Spec.Kafka.EventFilter,
		BucketFilter:  ft.Spec.Kafka.BucketFilter,
	}

	// Resolve SASL credentials from the referenced Secret.
	if ft.Spec.Kafka.SecretRef != nil {
		var secret corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: ft.Namespace,
			Name:      ft.Spec.Kafka.SecretRef.Name,
		}, &secret); err != nil {
			return fmt.Errorf("fetch kafka secret %q: %w", ft.Spec.Kafka.SecretRef.Name, err)
		}
		cfg.SASLUsername = string(secret.Data["username"])
		cfg.SASLPassword = string(secret.Data["password"])
		cfg.SASLMechanism = string(secret.Data["mechanism"])
	}

	r.KafkaConsumer.Upsert(key, ft.Namespace, ft.Name, cfg)
	return nil
}

func (r *WeaveTriggerReconciler) consumePendingKafkaFires(key string) ([]trigger.KafkaFireRequest, bool) {
	r.pendingKafkaFiresMu.Lock()
	defer r.pendingKafkaFiresMu.Unlock()
	fires, ok := r.pendingKafkaFires[key]
	if ok {
		delete(r.pendingKafkaFires, key)
	}
	return fires, ok
}

// maybeCreateKafkaRun applies the MaxConcurrentRuns throttle and creates a WeaveRun.
// When the cap is reached the fire is silently dropped — the Kafka offset was already
// committed by the consumer goroutine so this event will not be replayed.
func (r *WeaveTriggerReconciler) maybeCreateKafkaRun(
	ctx context.Context,
	ft *weavev1alpha1.WeaveTrigger,
	chain *weavev1alpha1.WeaveChain,
	fire trigger.KafkaFireRequest,
) error {
	if ft.Spec.Kafka != nil && ft.Spec.Kafka.MaxConcurrentRuns > 0 {
		var runList weavev1alpha1.WeaveRunList
		if err := r.List(ctx, &runList,
			client.InNamespace(ft.Namespace),
			client.MatchingLabels{labelTrigger: ft.Name},
		); err != nil {
			return err
		}
		active := 0
		for _, run := range runList.Items {
			switch run.Status.Phase {
			case weavev1alpha1.RunPhasePending, weavev1alpha1.RunPhaseRunning, "":
				active++
			}
		}
		if active >= ft.Spec.Kafka.MaxConcurrentRuns {
			log.FromContext(ctx).Info("kafka run throttled",
				"trigger", ft.Name,
				"active", active,
				"max", ft.Spec.Kafka.MaxConcurrentRuns,
			)
			return nil
		}
	}
	return r.createKafkaRun(ctx, ft, fire)
}

func (r *WeaveTriggerReconciler) createKafkaRun(
	ctx context.Context,
	ft *weavev1alpha1.WeaveTrigger,
	fire trigger.KafkaFireRequest,
) error {
	merged := mergeEnvVars(ft.Spec.ParameterOverrides, fire.EnvVars)

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
		return fmt.Errorf("create kafka WeaveRun: %w", err)
	}

	now := metav1.Now()
	patch := client.MergeFrom(ft.DeepCopy())
	ft.Status.LastScheduleTime = &now
	ft.Status.LastRunName = run.Name
	return r.Status().Patch(ctx, ft, patch)
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

func (r *WeaveTriggerReconciler) consumePendingBatchFires(key string) ([]trigger.BatchFireRequest, bool) {
	r.pendingBatchFiresMu.Lock()
	defer r.pendingBatchFiresMu.Unlock()
	fires, ok := r.pendingBatchFires[key]
	if ok {
		delete(r.pendingBatchFires, key)
	}
	return fires, ok
}

// maybeCreateBatchRun checks per-job concurrency and creates a WeaveRun for a single
// batch job fire. Each job's concurrency is tracked independently using the batch-job-id label.
func (r *WeaveTriggerReconciler) maybeCreateBatchRun(
	ctx context.Context,
	ft *weavev1alpha1.WeaveTrigger,
	chain *weavev1alpha1.WeaveChain,
	fire trigger.BatchFireRequest,
) error {
	safeID := trigger.SanitizeJobID(fire.JobID)
	var runList weavev1alpha1.WeaveRunList
	if err := r.List(ctx, &runList,
		client.InNamespace(ft.Namespace),
		client.MatchingLabels{
			labelTrigger:    ft.Name,
			labelBatchJobID: safeID,
		},
	); err != nil {
		return err
	}

	for _, run := range runList.Items {
		phase := run.Status.Phase
		if phase == weavev1alpha1.RunPhasePending ||
			phase == weavev1alpha1.RunPhaseRunning ||
			phase == "" {
			// Active run for this job — skip regardless of ConcurrencyPolicy.
			// For BatchCron, Wait semantics would require complex re-queuing;
			// missing a cron tick is preferred over unbounded queue growth.
			return nil
		}
	}

	return r.createBatchRun(ctx, ft, fire)
}

func (r *WeaveTriggerReconciler) createBatchRun(
	ctx context.Context,
	ft *weavev1alpha1.WeaveTrigger,
	fire trigger.BatchFireRequest,
) error {
	merged := mergeEnvVars(ft.Spec.ParameterOverrides, fire.ParameterOverrides)

	safeID := trigger.SanitizeJobID(fire.JobID)
	run := &weavev1alpha1.WeaveRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: ft.Name + "-" + safeID + "-",
			Namespace:    ft.Namespace,
			Labels: map[string]string{
				labelTrigger:    ft.Name,
				labelChain:      ft.Spec.ChainRef.Name,
				labelBatchJobID: safeID,
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
		return fmt.Errorf("create batch WeaveRun: %w", err)
	}

	now := metav1.Now()
	patch := client.MergeFrom(ft.DeepCopy())
	ft.Status.LastScheduleTime = &now
	ft.Status.LastRunName = run.Name
	return r.Status().Patch(ctx, ft, patch)
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

	// Watch ConfigMaps labelled with batch-trigger so that YAML changes
	// immediately re-sync the BatchCronScheduler for the affected trigger.
	enqueueFromConfigMap := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
		triggerName, ok := obj.GetLabels()[labelBatchTrigger]
		if !ok {
			return nil
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Namespace: obj.GetNamespace(),
				Name:      triggerName,
			},
		}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&weavev1alpha1.WeaveTrigger{}).
		Watches(&weavev1alpha1.WeaveRun{}, enqueueFromRun).
		Watches(&corev1.ConfigMap{}, enqueueFromConfigMap).
		WatchesRawSource(wakeupSource).
		Complete(r)
}
