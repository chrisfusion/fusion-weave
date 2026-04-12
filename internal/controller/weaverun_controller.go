// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/dag"
	"fusion-platform.io/fusion-weave/internal/deploybuilder"
	"fusion-platform.io/fusion-weave/internal/jobbuilder"
)

// weaveRunGVK is the GVK used when constructing owner references.
// r.Get zeroes out TypeMeta on returned objects, so we set it explicitly.
var weaveRunGVK = schema.GroupVersionKind{
	Group:   weavev1alpha1.GroupVersion.Group,
	Version: weavev1alpha1.GroupVersion.Version,
	Kind:    "WeaveRun",
}

// WeaveRunReconciler executes the DAG of a WeaveRun by managing batch/v1 Jobs.
type WeaveRunReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	KubeClient kubernetes.Interface
}

// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=weaveruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=weaveruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete

func (r *WeaveRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var run weavev1alpha1.WeaveRun
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// C1 fix: capture the patch base immediately after Get, before any mutation.
	base := client.MergeFrom(run.DeepCopy())

	// Terminal runs need no further action.
	if isTerminal(run.Status.Phase) {
		return ctrl.Result{}, nil
	}

	// Pending runs are waiting for a concurrency slot from the trigger controller.
	if run.Status.Phase == weavev1alpha1.RunPhasePending {
		return ctrl.Result{}, nil
	}

	// First reconcile after creation: move to Running.
	if run.Status.Phase == "" {
		now := metav1.Now()
		run.Status.Phase = weavev1alpha1.RunPhaseRunning
		run.Status.StartTime = &now
		if err := r.Status().Patch(ctx, &run, base); err != nil {
			return ctrl.Result{}, fmt.Errorf("set running: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Prepare a TypeMeta-aware copy for owner references on Jobs/PVCs (C4 fix pattern).
	runWithGVK := run.DeepCopy()
	runWithGVK.TypeMeta = metav1.TypeMeta{
		APIVersion: weaveRunGVK.GroupVersion().String(),
		Kind:       weaveRunGVK.Kind,
	}

	// Load the chain.
	var chain weavev1alpha1.WeaveChain
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: run.Namespace, Name: run.Spec.ChainRef.Name,
	}, &chain); err != nil {
		return ctrl.Result{}, fmt.Errorf("get chain: %w", err)
	}

	// Provision the per-run shared PVC when the chain requests shared storage
	// and it has not yet been recorded in status.
	if chain.Spec.SharedStorage != nil && run.Status.SharedPVCName == "" {
		pvcName, pvcErr := r.ensureSharedPVC(ctx, &run, runWithGVK, chain.Spec.SharedStorage)
		if pvcErr != nil {
			return ctrl.Result{}, pvcErr
		}
		run.Status.SharedPVCName = pvcName
		if err := r.Status().Patch(ctx, &run, base); err != nil {
			return ctrl.Result{}, fmt.Errorf("record shared PVC name: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Load all referenced job templates (deduplicated), skipping deploy-kind steps.
	templates := map[string]*weavev1alpha1.WeaveJobTemplate{}
	serviceTemplates := map[string]*weavev1alpha1.WeaveServiceTemplate{}
	for _, step := range chain.Spec.Steps {
		kind := step.StepKind
		if kind == "" {
			kind = weavev1alpha1.StepKindJob
		}
		switch kind {
		case weavev1alpha1.StepKindJob:
			if step.JobTemplateRef == nil {
				continue
			}
			if _, ok := templates[step.JobTemplateRef.Name]; ok {
				continue
			}
			var tmpl weavev1alpha1.WeaveJobTemplate
			if err := r.Get(ctx, types.NamespacedName{
				Namespace: run.Namespace, Name: step.JobTemplateRef.Name,
			}, &tmpl); err != nil {
				return ctrl.Result{}, fmt.Errorf("get template %q: %w", step.JobTemplateRef.Name, err)
			}
			templates[step.JobTemplateRef.Name] = &tmpl

		case weavev1alpha1.StepKindDeploy:
			if step.ServiceTemplateRef == nil {
				continue
			}
			if _, ok := serviceTemplates[step.ServiceTemplateRef.Name]; ok {
				continue
			}
			var tmpl weavev1alpha1.WeaveServiceTemplate
			if err := r.Get(ctx, types.NamespacedName{
				Namespace: run.Namespace, Name: step.ServiceTemplateRef.Name,
			}, &tmpl); err != nil {
				return ctrl.Result{}, fmt.Errorf("get service template %q: %w", step.ServiceTemplateRef.Name, err)
			}
			serviceTemplates[step.ServiceTemplateRef.Name] = &tmpl
		}
	}

	// Build the DAG graph.
	nodes := make([]dag.Node, len(chain.Spec.Steps))
	for i, s := range chain.Spec.Steps {
		nodes[i] = dag.Node{
			Name:         s.Name,
			DependsOn:    s.DependsOn,
			RunOnSuccess: s.RunOnSuccess,
			RunOnFailure: s.RunOnFailure,
		}
	}
	graph, err := dag.BuildGraph(nodes)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("build dag: %w", err)
	}

	// C3 fix: build the working step map from value copies, not slice pointers.
	stepStates := make(map[string]dag.StepPhase, len(chain.Spec.Steps))
	stepMap := make(map[string]weavev1alpha1.WeaveRunStepStatus, len(run.Status.Steps))
	for _, s := range run.Status.Steps {
		stepMap[s.Name] = s
		stepStates[s.Name] = dag.StepPhase(s.Phase)
	}

	// Sync running steps from their batch Jobs or Deployments.
	requeueAfter := time.Duration(0)
	for name, ss := range stepMap {
		if ss.Phase != weavev1alpha1.StepPhaseRunning && ss.Phase != weavev1alpha1.StepPhaseRetrying {
			continue
		}

		// Deploy-kind step: check Deployment availability.
		if ss.DeploymentRef != nil {
			var deploy appsv1.Deployment
			deployErr := r.Get(ctx, types.NamespacedName{
				Namespace: run.Namespace, Name: ss.DeploymentRef.Name,
			}, &deploy)
			if deployErr != nil {
				if !errors.IsNotFound(deployErr) {
					return ctrl.Result{}, deployErr
				}
				// Deployment disappeared — requeue.
				if requeueAfter == 0 || 5*time.Second < requeueAfter {
					requeueAfter = 5 * time.Second
				}
				continue
			}
			if isDeploymentAvailable(&deploy) {
				now := metav1.Now()
				ss.Phase = weavev1alpha1.StepPhaseSucceeded
				ss.CompletionTime = &now
				stepStates[name] = dag.StepPhaseSucceeded
				stepMap[name] = ss
				// Register the deployment for ongoing health monitoring on the chain.
				stepSpec := findStepSpec(chain.Spec.Steps, name)
				if stepSpec != nil && stepSpec.ServiceTemplateRef != nil {
					svcTmpl := serviceTemplates[stepSpec.ServiceTemplateRef.Name]
					if svcTmpl != nil {
						if regErr := r.registerActiveDeployment(ctx, &chain, name, deploy.Name, svcTmpl); regErr != nil {
							logger.Error(regErr, "register active deployment", "step", name)
						}
					}
				}
			} else {
				// Not available yet — requeue to poll.
				if requeueAfter == 0 || 10*time.Second < requeueAfter {
					requeueAfter = 10 * time.Second
				}
			}
			continue
		}

		if ss.Phase == weavev1alpha1.StepPhaseRetrying {
			if ss.NextRetryAfter != nil && time.Now().Before(ss.NextRetryAfter.Time) {
				wait := time.Until(ss.NextRetryAfter.Time)
				if requeueAfter == 0 || wait < requeueAfter {
					requeueAfter = wait
				}
				continue
			}
			// Backoff elapsed — promote to Pending so Advance will start it.
			ss.Phase = weavev1alpha1.StepPhasePending
			stepStates[name] = dag.StepPhasePending
			stepMap[name] = ss
			continue
		}

		// Job-kind step: fetch the batch Job.
		if ss.JobRef == nil {
			continue
		}
		var job batchv1.Job
		jobErr := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: ss.JobRef.Name}, &job)
		if errors.IsNotFound(jobErr) {
			ss.Phase = weavev1alpha1.StepPhaseFailed
			ss.Message = "batch Job was deleted externally"
			stepStates[name] = dag.StepPhaseFailed
			stepMap[name] = ss
			continue
		}
		if jobErr != nil {
			return ctrl.Result{}, jobErr
		}

		if isJobSucceeded(&job) {
			stepSpec := findStepSpec(chain.Spec.Steps, name)
			if stepSpec != nil && stepSpec.ProducesOutput && !ss.OutputCaptured {
				// Capture JSON stdout before marking this step succeeded.
				jsonData, podFound, captureErr := r.captureStepOutput(ctx, run.Namespace, ss.JobRef.Name)
				if captureErr != nil {
					now := metav1.Now()
					ss.Phase = weavev1alpha1.StepPhaseFailed
					ss.CompletionTime = &now
					ss.Message = captureErr.Error()
					stepStates[name] = dag.StepPhaseFailed
					stepMap[name] = ss
					continue
				}
				if !podFound {
					if requeueAfter == 0 || 2*time.Second < requeueAfter {
						requeueAfter = 2 * time.Second
					}
					continue
				}
				if writeErr := r.writeOutputToConfigMap(ctx, &run, runWithGVK, name, jsonData); writeErr != nil {
					return ctrl.Result{}, fmt.Errorf("write output for step %q: %w", name, writeErr)
				}
				ss.OutputCaptured = true
			}

			now := metav1.Now()
			ss.Phase = weavev1alpha1.StepPhaseSucceeded
			ss.CompletionTime = &now
			stepStates[name] = dag.StepPhaseSucceeded
			stepMap[name] = ss
		} else if isJobFailed(&job) {
			stepSpec := findStepSpec(chain.Spec.Steps, name)
			if stepSpec == nil {
				ss.Phase = weavev1alpha1.StepPhaseFailed
				ss.Message = "step removed from chain after run started"
				stepStates[name] = dag.StepPhaseFailed
				stepMap[name] = ss
				continue
			}
			if stepSpec.JobTemplateRef == nil {
				ss.Phase = weavev1alpha1.StepPhaseFailed
				ss.Message = "step has no jobTemplateRef"
				stepStates[name] = dag.StepPhaseFailed
				stepMap[name] = ss
				continue
			}
			tmpl := templates[stepSpec.JobTemplateRef.Name]
			maxRetries := int32(0)
			backoff := int32(10)
			if tmpl != nil && tmpl.Spec.RetryPolicy != nil {
				maxRetries = tmpl.Spec.RetryPolicy.MaxRetries
				backoff = tmpl.Spec.RetryPolicy.BackoffSeconds
			}

			if ss.RetryCount < maxRetries {
				_ = r.Delete(ctx, &job)
				ss.RetryCount++
				retryAt := metav1.NewTime(time.Now().Add(time.Duration(backoff) * time.Second))
				ss.NextRetryAfter = &retryAt
				ss.JobRef = nil
				ss.Phase = weavev1alpha1.StepPhaseRetrying
				stepStates[name] = dag.StepPhaseRetrying
				if requeueAfter == 0 || time.Duration(backoff)*time.Second < requeueAfter {
					requeueAfter = time.Duration(backoff) * time.Second
				}
			} else {
				now := metav1.Now()
				ss.Phase = weavev1alpha1.StepPhaseFailed
				ss.CompletionTime = &now
				ss.Message = "max retries exhausted"
				stepStates[name] = dag.StepPhaseFailed
			}
			stepMap[name] = ss
		}
	}

	// Run the DAG executor.
	advancement := dag.Advance(graph, stepStates, dag.FailurePolicy(chain.Spec.FailurePolicy))

	// Apply executor decisions.
	for stepName, decision := range advancement.Decisions {
		switch decision {
		case dag.DecisionStart:
			stepSpec := findStepSpec(chain.Spec.Steps, stepName)
			if stepSpec == nil {
				continue
			}
			ss := getOrCreateStep(stepMap, stepName)

			kind := stepSpec.StepKind
			if kind == "" {
				kind = weavev1alpha1.StepKindJob
			}

			switch kind {
			case weavev1alpha1.StepKindDeploy:
				if stepSpec.ServiceTemplateRef == nil {
					continue
				}
				svcTmpl := serviceTemplates[stepSpec.ServiceTemplateRef.Name]
				if svcTmpl == nil {
					continue
				}
				if err := r.syncDeployStep(ctx, &chain, &run, stepSpec, svcTmpl, &ss); err != nil {
					return ctrl.Result{}, fmt.Errorf("sync deploy step %q: %w", stepName, err)
				}
				// Poll every 10s while waiting for the Deployment to become Available.
				if requeueAfter == 0 || 10*time.Second < requeueAfter {
					requeueAfter = 10 * time.Second
				}

			case weavev1alpha1.StepKindJob:
				if stepSpec.JobTemplateRef == nil {
					continue
				}
				tmpl := templates[stepSpec.JobTemplateRef.Name]
				if tmpl == nil {
					continue
				}

				// Prepare merged input JSON if this step consumes upstream outputs.
				inputConfigMap := ""
				if len(stepSpec.ConsumesOutputFrom) > 0 {
					cmName, ready, prepErr := r.prepareInputData(ctx, &run, stepSpec)
					if prepErr != nil {
						return ctrl.Result{}, fmt.Errorf("prepare input for step %q: %w", stepName, prepErr)
					}
					if !ready {
						if requeueAfter == 0 || 2*time.Second < requeueAfter {
							requeueAfter = 2 * time.Second
						}
						continue
					}
					inputConfigMap = cmName
				}

				job := jobbuilder.Build(tmpl, stepSpec, &run, ss.RetryCount, inputConfigMap, run.Status.SharedPVCName)
				job.OwnerReferences = []metav1.OwnerReference{
					*metav1.NewControllerRef(runWithGVK, weaveRunGVK),
				}
				if err := r.Create(ctx, job); err != nil && !errors.IsAlreadyExists(err) {
					return ctrl.Result{}, fmt.Errorf("create job for step %q: %w", stepName, err)
				}
				if ss.StartTime == nil {
					now := metav1.Now()
					ss.StartTime = &now
				}
				ss.Phase = weavev1alpha1.StepPhaseRunning
				ref := corev1.LocalObjectReference{Name: job.Name}
				ss.JobRef = &ref
				stepStates[stepName] = dag.StepPhaseRunning
			}
			stepMap[stepName] = ss

		case dag.DecisionSkip:
			ss := getOrCreateStep(stepMap, stepName)
			if ss.Phase != weavev1alpha1.StepPhaseSkipped {
				now := metav1.Now()
				ss.Phase = weavev1alpha1.StepPhaseSkipped
				ss.CompletionTime = &now
				stepMap[stepName] = ss
			}
		}
	}

	// H1 fix: rebuild the steps slice in deterministic (alphabetical) order.
	stepNames := make([]string, 0, len(stepMap))
	for name := range stepMap {
		stepNames = append(stepNames, name)
	}
	sort.Strings(stepNames)
	newSteps := make([]weavev1alpha1.WeaveRunStepStatus, 0, len(stepNames))
	for _, name := range stepNames {
		newSteps = append(newSteps, stepMap[name])
	}
	run.Status.Steps = newSteps

	if advancement.RunComplete {
		now := metav1.Now()
		run.Status.CompletionTime = &now
		if advancement.RunSucceeded {
			run.Status.Phase = weavev1alpha1.RunPhaseSucceeded
			logger.Info("WeaveRun succeeded")
		} else if chain.Spec.FailurePolicy == weavev1alpha1.FailurePolicyStopAll {
			run.Status.Phase = weavev1alpha1.RunPhaseStopped
			logger.Info("WeaveRun stopped by StopAll policy")
		} else {
			run.Status.Phase = weavev1alpha1.RunPhaseFailed
			logger.Info("WeaveRun failed")
		}
	}

	if err := r.Status().Patch(ctx, &run, base); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch status: %w", err)
	}

	if requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// syncDeployStep creates or rolling-updates the Deployment, Service, and
// optional Ingress for a deploy-kind step. Owner of all three is the WeaveChain
// so they persist across WeaveRun deletions.
func (r *WeaveRunReconciler) syncDeployStep(
	ctx context.Context,
	chain *weavev1alpha1.WeaveChain,
	run *weavev1alpha1.WeaveRun,
	stepSpec *weavev1alpha1.WeaveChainStep,
	svcTmpl *weavev1alpha1.WeaveServiceTemplate,
	ss *weavev1alpha1.WeaveRunStepStatus,
) error {
	chainWithGVK := chain.DeepCopy()
	chainWithGVK.TypeMeta = metav1.TypeMeta{
		APIVersion: weaveChainGVK.GroupVersion().String(),
		Kind:       weaveChainGVK.Kind,
	}
	ownerRef := metav1.NewControllerRef(chainWithGVK, weaveChainGVK)

	deployName := deploybuilder.DeploymentName(chain.Name, stepSpec.Name)

	// Upsert Deployment.
	desired := deploybuilder.Build(svcTmpl, chain.Name, stepSpec.Name, run.Namespace)
	desired.OwnerReferences = []metav1.OwnerReference{*ownerRef}

	var existing appsv1.Deployment
	err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: deployName}, &existing)
	if errors.IsNotFound(err) {
		if createErr := r.Create(ctx, desired); createErr != nil && !errors.IsAlreadyExists(createErr) {
			return fmt.Errorf("create deployment %q: %w", deployName, createErr)
		}
	} else if err == nil {
		// Rolling update: patch spec.template and spec.replicas only.
		patch := client.MergeFrom(existing.DeepCopy())
		existing.Spec.Template = desired.Spec.Template
		existing.Spec.Replicas = desired.Spec.Replicas
		if patchErr := r.Patch(ctx, &existing, patch); patchErr != nil {
			return fmt.Errorf("patch deployment %q: %w", deployName, patchErr)
		}
	} else {
		return fmt.Errorf("get deployment %q: %w", deployName, err)
	}

	// Upsert Service.
	svc := deploybuilder.BuildService(svcTmpl, chain.Name, stepSpec.Name, run.Namespace)
	svc.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	if err := r.upsertService(ctx, svc); err != nil {
		return fmt.Errorf("upsert service %q: %w", svc.Name, err)
	}

	// Upsert Ingress if configured.
	if svcTmpl.Spec.Ingress != nil {
		ing := deploybuilder.BuildIngress(svcTmpl, chain.Name, stepSpec.Name, run.Namespace)
		if ing != nil {
			ing.OwnerReferences = []metav1.OwnerReference{*ownerRef}
			if err := r.upsertIngress(ctx, ing); err != nil {
				return fmt.Errorf("upsert ingress %q: %w", ing.Name, err)
			}
		}
	}

	if ss.StartTime == nil {
		now := metav1.Now()
		ss.StartTime = &now
	}
	ss.Phase = weavev1alpha1.StepPhaseRunning
	ss.DeploymentRef = &corev1.LocalObjectReference{Name: deployName}
	return nil
}

// upsertService creates a Service or patches it if it already exists.
func (r *WeaveRunReconciler) upsertService(ctx context.Context, desired *corev1.Service) error {
	var existing corev1.Service
	err := r.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing)
	if errors.IsNotFound(err) {
		if createErr := r.Create(ctx, desired); createErr != nil && !errors.IsAlreadyExists(createErr) {
			return createErr
		}
		return nil
	}
	if err != nil {
		return err
	}
	patch := client.MergeFrom(existing.DeepCopy())
	existing.Spec.Ports = desired.Spec.Ports
	existing.Spec.Type = desired.Spec.Type
	return r.Patch(ctx, &existing, patch)
}

// upsertIngress creates an Ingress or patches it if it already exists.
func (r *WeaveRunReconciler) upsertIngress(ctx context.Context, desired *networkingv1.Ingress) error {
	var existing networkingv1.Ingress
	err := r.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing)
	if errors.IsNotFound(err) {
		if createErr := r.Create(ctx, desired); createErr != nil && !errors.IsAlreadyExists(createErr) {
			return createErr
		}
		return nil
	}
	if err != nil {
		return err
	}
	patch := client.MergeFrom(existing.DeepCopy())
	existing.Spec = desired.Spec
	return r.Patch(ctx, &existing, patch)
}

// registerActiveDeployment adds (or updates) an entry in WeaveChain.Status.ActiveDeployments
// so the chain controller can monitor the Deployment's ongoing health.
func (r *WeaveRunReconciler) registerActiveDeployment(
	ctx context.Context,
	chain *weavev1alpha1.WeaveChain,
	stepName, deploymentName string,
	svcTmpl *weavev1alpha1.WeaveServiceTemplate,
) error {
	// Cache unhealthyDurationSeconds from the template so health loop doesn't need a lookup.
	dur, err := time.ParseDuration(svcTmpl.Spec.UnhealthyDuration)
	if err != nil {
		dur = 5 * time.Minute
	}

	entry := weavev1alpha1.WeaveActiveDeploymentStatus{
		DeploymentName:           deploymentName,
		StepName:                 stepName,
		Health:                   weavev1alpha1.DeployHealthHealthy,
		UnhealthyDurationSeconds: int64(dur.Seconds()),
	}

	patch := client.MergeFrom(chain.DeepCopy())
	if chain.Status.ActiveDeployments == nil {
		chain.Status.ActiveDeployments = map[string]weavev1alpha1.WeaveActiveDeploymentStatus{}
	}
	chain.Status.ActiveDeployments[deploymentName] = entry
	return r.Status().Patch(ctx, chain, patch)
}

// captureStepOutput fetches and validates JSON stdout from the completed pod of the given job.
func (r *WeaveRunReconciler) captureStepOutput(ctx context.Context, ns, jobName string) (string, bool, error) {
	podList, err := r.KubeClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + jobName,
	})
	if err != nil {
		return "", true, fmt.Errorf("list pods for job %q: %w", jobName, err)
	}

	var completedPod *corev1.Pod
	for i := range podList.Items {
		if podList.Items[i].Status.Phase == corev1.PodSucceeded {
			completedPod = &podList.Items[i]
			break
		}
	}
	if completedPod == nil {
		return "", false, nil
	}

	logBytes, err := r.KubeClient.CoreV1().Pods(ns).
		GetLogs(completedPod.Name, &corev1.PodLogOptions{Container: "job"}).
		DoRaw(ctx)
	if err != nil {
		return "", true, fmt.Errorf("get logs for pod %q: %w", completedPod.Name, err)
	}

	trimmed := strings.TrimSpace(string(logBytes))
	if !json.Valid([]byte(trimmed)) {
		return "", true, fmt.Errorf("output is not valid JSON")
	}
	return trimmed, true, nil
}

// writeOutputToConfigMap creates (if needed) the run's output ConfigMap and writes
// the captured JSON for the named step under its output key.
func (r *WeaveRunReconciler) writeOutputToConfigMap(ctx context.Context, run *weavev1alpha1.WeaveRun, runWithGVK *weavev1alpha1.WeaveRun, stepName, jsonData string) error {
	cmName := jobbuilder.OutputsConfigMapName(run.Name)
	key := jobbuilder.OutputConfigMapKey(stepName)

	var cm corev1.ConfigMap
	err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: cmName}, &cm)
	if errors.IsNotFound(err) {
		cm = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: run.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(runWithGVK, weaveRunGVK),
				},
			},
			Data: map[string]string{key: jsonData},
		}
		createErr := r.Create(ctx, &cm)
		if errors.IsAlreadyExists(createErr) {
			if getErr := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: cmName}, &cm); getErr != nil {
				return getErr
			}
			patch := client.MergeFrom(cm.DeepCopy())
			if cm.Data == nil {
				cm.Data = map[string]string{}
			}
			cm.Data[key] = jsonData
			return r.Patch(ctx, &cm, patch)
		}
		return createErr
	}
	if err != nil {
		return err
	}
	patch := client.MergeFrom(cm.DeepCopy())
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[key] = jsonData
	return r.Patch(ctx, &cm, patch)
}

// ensureSharedPVC creates the per-run shared PVC if it does not already exist.
func (r *WeaveRunReconciler) ensureSharedPVC(
	ctx context.Context,
	run *weavev1alpha1.WeaveRun,
	runWithGVK *weavev1alpha1.WeaveRun,
	storageSpec *weavev1alpha1.WeaveSharedStorageSpec,
) (string, error) {
	pvcName := jobbuilder.SharedPVCName(run.Name)

	storageQty, err := resource.ParseQuantity(storageSpec.Size)
	if err != nil {
		return "", fmt.Errorf("parse shared storage size %q: %w", storageSpec.Size, err)
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: run.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(runWithGVK, weaveRunGVK),
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			StorageClassName: storageSpec.StorageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageQty,
				},
			},
		},
	}

	if createErr := r.Create(ctx, pvc); createErr != nil && !errors.IsAlreadyExists(createErr) {
		return "", fmt.Errorf("create shared PVC %q: %w", pvcName, createErr)
	}
	return pvcName, nil
}

// prepareInputData reads the captured outputs of all upstream steps, merges them
// into a namespaced JSON object and writes the result to the run's output ConfigMap.
func (r *WeaveRunReconciler) prepareInputData(ctx context.Context, run *weavev1alpha1.WeaveRun, stepSpec *weavev1alpha1.WeaveChainStep) (string, bool, error) {
	cmName := jobbuilder.OutputsConfigMapName(run.Name)
	inputKey := jobbuilder.InputConfigMapKey(stepSpec.Name)

	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: cmName}, &cm); err != nil {
		if errors.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("get outputs ConfigMap: %w", err)
	}

	if _, exists := cm.Data[inputKey]; exists {
		return cmName, true, nil
	}

	merged := map[string]interface{}{}
	for _, producerName := range stepSpec.ConsumesOutputFrom {
		raw, ok := cm.Data[jobbuilder.OutputConfigMapKey(producerName)]
		if !ok {
			return "", false, nil
		}
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			return "", false, fmt.Errorf("parse output of step %q: %w", producerName, err)
		}
		merged[producerName] = obj
	}

	mergedBytes, err := json.Marshal(merged)
	if err != nil {
		return "", false, fmt.Errorf("marshal merged input: %w", err)
	}

	patch := client.MergeFrom(cm.DeepCopy())
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[inputKey] = string(mergedBytes)
	if err := r.Patch(ctx, &cm, patch); err != nil {
		return "", false, fmt.Errorf("write input key to ConfigMap: %w", err)
	}
	return cmName, true, nil
}

func getOrCreateStep(stepMap map[string]weavev1alpha1.WeaveRunStepStatus, name string) weavev1alpha1.WeaveRunStepStatus {
	if ss, ok := stepMap[name]; ok {
		return ss
	}
	return weavev1alpha1.WeaveRunStepStatus{
		Name:  name,
		Phase: weavev1alpha1.StepPhasePending,
	}
}

func isTerminal(phase weavev1alpha1.WeaveRunPhase) bool {
	return phase == weavev1alpha1.RunPhaseSucceeded ||
		phase == weavev1alpha1.RunPhaseFailed ||
		phase == weavev1alpha1.RunPhaseStopped
}

func isJobSucceeded(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == "True" {
			return true
		}
	}
	return false
}

func isJobFailed(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == "True" {
			return true
		}
	}
	return false
}

func findStepSpec(steps []weavev1alpha1.WeaveChainStep, name string) *weavev1alpha1.WeaveChainStep {
	for i := range steps {
		if steps[i].Name == name {
			return &steps[i]
		}
	}
	return nil
}

func (r *WeaveRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Enqueue the owning WeaveRun when a child batch Job changes.
	enqueueFromJob := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		for _, ref := range obj.GetOwnerReferences() {
			if ref.Kind == "WeaveRun" {
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Namespace: obj.GetNamespace(),
						Name:      ref.Name,
					},
				}}
			}
		}
		return nil
	})

	// Enqueue any running WeaveRun that has a deploy step referencing this Deployment.
	enqueueFromDeployment := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		deployName := obj.GetName()
		ns := obj.GetNamespace()

		var runList weavev1alpha1.WeaveRunList
		if err := r.List(ctx, &runList, client.InNamespace(ns)); err != nil {
			return nil
		}
		var reqs []reconcile.Request
		for _, run := range runList.Items {
			if isTerminal(run.Status.Phase) {
				continue
			}
			for _, ss := range run.Status.Steps {
				if ss.DeploymentRef != nil && ss.DeploymentRef.Name == deployName &&
					ss.Phase == weavev1alpha1.StepPhaseRunning {
					reqs = append(reqs, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Namespace: ns,
							Name:      run.Name,
						},
					})
					break
				}
			}
		}
		return reqs
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&weavev1alpha1.WeaveRun{}).
		Watches(&batchv1.Job{}, enqueueFromJob).
		Watches(&appsv1.Deployment{}, enqueueFromDeployment).
		Complete(r)
}
