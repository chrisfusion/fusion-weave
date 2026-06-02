// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/dag"
	"fusion-platform.io/fusion-weave/internal/deploybuilder"
	"fusion-platform.io/fusion-weave/internal/indexclient"
	"fusion-platform.io/fusion-weave/internal/jobbuilder"
	"fusion-platform.io/fusion-weave/internal/security"
)

const annotationRestartStep = "fusion-platform.io/restart-step"

// finalizerDeployCleanup is added to any WeaveRun whose chain contains deploy-kind
// steps. It ensures the Reconciler can delete the Deployment/Service/Ingress before
// Kubernetes GC removes the WeaveRun object.
const finalizerDeployCleanup = "weave.fusion-platform.io/deploy-cleanup"

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
	Scheme                 *runtime.Scheme
	KubeClient             kubernetes.Interface
	SecurityDefaults       security.Defaults
	CodeSourcePollInterval time.Duration
	FusionIndexURL         string
	LoaderImage            string
	WritablePaths          []string
}

// failStepNow marks a step Failed with a message and records the completion time.
func failStepNow(ss *weavev1alpha1.WeaveRunStepStatus, msg string) {
	now := metav1.Now()
	ss.Phase = weavev1alpha1.StepPhaseFailed
	ss.Message = msg
	ss.CompletionTime = &now
}

// jobFailureMessage builds a human-readable failure reason from the batch Job's
// conditions and the first failed pod's termination state. For init-container
// failures (e.g. the code-loader) it includes a tail of the container logs so
// the root cause (bad INDEX_URL, missing artifact, etc.) is visible in status.
func (r *WeaveRunReconciler) jobFailureMessage(ctx context.Context, namespace string, job *batchv1.Job) string {
	// Collect Job-level condition message — used as base, not short-circuit.
	condMsg := ""
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Message != "" {
			condMsg = c.Message
			break
		}
	}

	// List pods and check termination state for richer detail.
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(namespace),
		client.MatchingLabels{"job-name": job.Name},
	); err != nil {
		if condMsg != "" {
			return condMsg
		}
		return "job failed (pod details unavailable)"
	}

	for _, pod := range pods.Items {
		// Init containers first — code-loader exits 1 on bad INDEX_URL / missing artifact.
		for _, cs := range pod.Status.InitContainerStatuses {
			if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
				tail := r.podLogTail(ctx, namespace, pod.Name, cs.Name, 10)
				if tail != "" {
					return fmt.Sprintf("init container %q exited %d:\n%s",
						cs.Name, cs.State.Terminated.ExitCode, tail)
				}
				return fmt.Sprintf("init container %q exited %d", cs.Name, cs.State.Terminated.ExitCode)
			}
		}
		// Main container — append exit code to condition message when available.
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
				detail := fmt.Sprintf("container %q exited %d", cs.Name, cs.State.Terminated.ExitCode)
				if cs.State.Terminated.Message != "" {
					detail += ": " + cs.State.Terminated.Message
				}
				if condMsg != "" {
					return condMsg + " (" + detail + ")"
				}
				return detail
			}
		}
	}
	if condMsg != "" {
		return condMsg
	}
	return "job failed"
}

// deploymentPodFailureMessage detects CrashLoopBackOff or repeated init-container
// failures in a Deployment's pods. Returns a non-empty message when the failure
// is confirmed (restartCount >= 2 or CrashLoopBackOff), empty when it's too early
// to call. Used to surface loader errors (bad INDEX_URL etc.) for deploy steps.
func (r *WeaveRunReconciler) deploymentPodFailureMessage(ctx context.Context, namespace string, deploy *appsv1.Deployment) string {
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(namespace),
		client.MatchingLabels(deploy.Spec.Selector.MatchLabels),
	); err != nil {
		return ""
	}
	for _, pod := range pods.Items {
		// Init containers: report after ≥2 restarts to filter transient failures.
		for _, cs := range pod.Status.InitContainerStatuses {
			crashed := cs.RestartCount >= 2 ||
				(cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff")
			if !crashed {
				continue
			}
			exitCode := containerExitCode(cs)
			tail := r.podLogTail(ctx, namespace, pod.Name, cs.Name, 10)
			if tail != "" {
				return fmt.Sprintf("init container %q crash-looping (exit %d):\n%s", cs.Name, exitCode, tail)
			}
			return fmt.Sprintf("init container %q crash-looping (exit %d)", cs.Name, exitCode)
		}
		// Main containers.
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
				msg := fmt.Sprintf("container %q crash-looping (exit %d)", cs.Name, containerExitCode(cs))
				if cs.State.Waiting.Message != "" {
					msg += ": " + cs.State.Waiting.Message
				}
				return msg
			}
		}
	}
	return ""
}

// containerExitCode returns the exit code from the last or current termination state of a container.
func containerExitCode(cs corev1.ContainerStatus) int32 {
	if cs.LastTerminationState.Terminated != nil {
		return cs.LastTerminationState.Terminated.ExitCode
	}
	if cs.State.Terminated != nil {
		return cs.State.Terminated.ExitCode
	}
	return -1
}

// podLogTail returns the last `lines` lines from a container's log.
// Tries the current run first; falls back to the previous run's logs (for
// CrashLoopBackOff where the container is waiting, not terminated).
// Returns an empty string on any error so callers can treat it as optional.
func (r *WeaveRunReconciler) podLogTail(ctx context.Context, namespace, podName, container string, lines int64) string {
	for _, previous := range []bool{false, true} {
		stream, err := r.KubeClient.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
			Container: container,
			TailLines: &lines,
			Previous:  previous,
		}).Stream(ctx)
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(stream)
		stream.Close()
		if s := strings.TrimSpace(string(data)); s != "" {
			return s
		}
	}
	return ""
}

// resolveIndexURL returns the effective fusion-index base URL for a given
// explicit CRD field value. Priority: explicit field → FUSION_INDEX_URL env
// var (r.FusionIndexURL) → hardcoded in-cluster default.
func (r *WeaveRunReconciler) resolveIndexURL(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if r.FusionIndexURL != "" {
		return r.FusionIndexURL
	}
	return "http://fusion-index-backend.fusion.svc.cluster.local:8080"
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

	// Handle deletion: clean up deploy-step resources before allowing GC.
	if !run.DeletionTimestamp.IsZero() {
		return r.handleRunDeletion(ctx, &run)
	}

	// Terminal runs: clean up any lingering deploy steps (handles direct status-patch
	// kills), then remove the finalizer so the run can be freely deleted.
	if isTerminal(run.Status.Phase) {
		if controllerutil.ContainsFinalizer(&run, finalizerDeployCleanup) {
			if run.Status.Phase != weavev1alpha1.RunPhaseSucceeded {
				if err := r.doDeployTeardown(ctx, &run); err != nil {
					return ctrl.Result{}, err
				}
			}
			finBase := client.MergeFrom(run.DeepCopy())
			controllerutil.RemoveFinalizer(&run, finalizerDeployCleanup)
			if err := r.Patch(ctx, &run, finBase); err != nil {
				return ctrl.Result{}, fmt.Errorf("remove deploy-cleanup finalizer: %w", err)
			}
		}
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
		if errors.IsNotFound(err) {
			msg := fmt.Sprintf("chain %q not found", run.Spec.ChainRef.Name)
			logger.Error(err, "chain not found — failing run", "chain", run.Spec.ChainRef.Name)
			run.Status.Phase = weavev1alpha1.RunPhaseFailed
			run.Status.Message = msg
			_ = r.Status().Patch(ctx, &run, base)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get chain: %w", err)
	}

	// Add the deploy-cleanup finalizer as soon as we confirm the chain has deploy
	// steps. Must happen before any Deployment is created so deletion is always
	// handled cleanly. Requeue after the Update so base is recaptured fresh.
	if !controllerutil.ContainsFinalizer(&run, finalizerDeployCleanup) {
		for _, step := range chain.Spec.Steps {
			if step.StepKind == weavev1alpha1.StepKindDeploy {
				finBase := client.MergeFrom(run.DeepCopy())
				controllerutil.AddFinalizer(&run, finalizerDeployCleanup)
				if err := r.Patch(ctx, &run, finBase); err != nil {
					return ctrl.Result{}, fmt.Errorf("add deploy-cleanup finalizer: %w", err)
				}
				return ctrl.Result{Requeue: true}, nil
			}
		}
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

	// Handle one-shot rolling restart of a deploy step.
	if restarted, err := r.handleRestartStep(ctx, &run); err != nil {
		return ctrl.Result{}, err
	} else if restarted {
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
				if errors.IsNotFound(err) {
					msg := fmt.Sprintf("job template %q not found (step %q)", step.JobTemplateRef.Name, step.Name)
					logger.Error(err, "job template not found — failing run", "template", step.JobTemplateRef.Name, "step", step.Name)
					run.Status.Phase = weavev1alpha1.RunPhaseFailed
					run.Status.Message = msg
					_ = r.Status().Patch(ctx, &run, base)
					return ctrl.Result{}, nil
				}
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
				if errors.IsNotFound(err) {
					msg := fmt.Sprintf("service template %q not found (step %q)", step.ServiceTemplateRef.Name, step.Name)
					logger.Error(err, "service template not found — failing run", "template", step.ServiceTemplateRef.Name, "step", step.Name)
					run.Status.Phase = weavev1alpha1.RunPhaseFailed
					run.Status.Message = msg
					_ = r.Status().Patch(ctx, &run, base)
					return ctrl.Result{}, nil
				}
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
		msg := fmt.Sprintf("invalid chain DAG: %v", err)
		logger.Error(err, "DAG build failed — failing run", "run", run.Name)
		run.Status.Phase = weavev1alpha1.RunPhaseFailed
		run.Status.Message = msg
		_ = r.Status().Patch(ctx, &run, base)
		return ctrl.Result{}, nil
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
		if ss.Phase != weavev1alpha1.StepPhaseRunning &&
			ss.Phase != weavev1alpha1.StepPhaseRetrying &&
			ss.Phase != weavev1alpha1.StepPhaseDeployed {
			continue
		}

		// Deploy-kind step: handle both the initial availability wait and ongoing health polling.
		if ss.DeploymentRef != nil {
			var deploy appsv1.Deployment
			deployErr := r.Get(ctx, types.NamespacedName{
				Namespace: run.Namespace, Name: ss.DeploymentRef.Name,
			}, &deploy)
			if deployErr != nil {
				if !errors.IsNotFound(deployErr) {
					return ctrl.Result{}, deployErr
				}
				if ss.Phase == weavev1alpha1.StepPhaseDeployed {
					// Service was running but its Deployment was deleted externally.
					now := metav1.Now()
					ss.Phase = weavev1alpha1.StepPhaseFailed
					ss.CompletionTime = &now
					ss.Message = "deployment was deleted externally"
					stepStates[name] = dag.StepPhaseFailed
					stepMap[name] = ss
				} else {
					// Deployment hasn't appeared yet — requeue.
					if requeueAfter == 0 || 5*time.Second < requeueAfter {
						requeueAfter = 5 * time.Second
					}
				}
				continue
			}
			if ss.Phase == weavev1alpha1.StepPhaseDeployed {
				override := findStepOverride(run.Spec.StepOverrides, name)
				if override != nil {
					// Run-owned deployment: handle code-source polling in this controller.
					pollInterval := r.CodeSourcePollInterval
					if pollInterval <= 0 {
						pollInterval = defaultCodeSourcePollInterval
					}
					if requeueAfter == 0 || pollInterval < requeueAfter {
						requeueAfter = pollInterval
					}
					if pollErr := r.pollRunDeploymentCodeSource(ctx, &run, ss.DeploymentRef.Name); pollErr != nil {
						logger.Error(pollErr, "code-source poll failed", "step", name)
					}
				} else {
					// Chain-owned deployment: health monitored by chain controller.
					if requeueAfter == 0 || 30*time.Second < requeueAfter {
						requeueAfter = 30 * time.Second
					}
				}
				continue
			}
			// Phase is Running — wait for the Deployment to become Available.
			if isDeploymentAvailable(&deploy) {
				ss.Phase = weavev1alpha1.StepPhaseDeployed
				stepStates[name] = dag.StepPhaseDeployed
				stepMap[name] = ss
				stepSpec := findStepSpec(chain.Spec.Steps, name)
				override := findStepOverride(run.Spec.StepOverrides, name)
				if override != nil {
					// Run-owned: register in run status for code-source polling.
					if stepSpec != nil && stepSpec.ServiceTemplateRef != nil {
						svcTmpl := serviceTemplates[stepSpec.ServiceTemplateRef.Name]
						if svcTmpl != nil {
							r.registerRunActiveDeployment(&run, name, deploy.Name, override, svcTmpl)
						}
					}
				} else {
					// Chain-owned: register in chain status for health monitoring.
					if stepSpec != nil && stepSpec.ServiceTemplateRef != nil {
						svcTmpl := serviceTemplates[stepSpec.ServiceTemplateRef.Name]
						if svcTmpl != nil {
							if regErr := r.registerActiveDeployment(ctx, &chain, name, deploy.Name, svcTmpl); regErr != nil {
								logger.Error(regErr, "register active deployment", "step", name)
							}
						}
					}
				}
				if requeueAfter == 0 || 30*time.Second < requeueAfter {
					requeueAfter = 30 * time.Second
				}
			} else {
				// Not available yet — check if pods are crash-looping (e.g. bad loader URL).
				if failMsg := r.deploymentPodFailureMessage(ctx, run.Namespace, &deploy); failMsg != "" {
					logger.Error(fmt.Errorf("%s", failMsg), "deploy step pods crash-looping", "step", name, "run", run.Name)
					failStepNow(&ss, failMsg)
					stepStates[name] = dag.StepPhaseFailed
					stepMap[name] = ss
					continue
				}
				// Requeue to poll.
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
					logger.Error(writeErr, "failed to write step output", "step", name, "run", run.Name)
					failStepNow(&ss, fmt.Sprintf("failed to write output: %v", writeErr))
					stepStates[name] = dag.StepPhaseFailed
					stepMap[name] = ss
					continue
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

			failureReason := r.jobFailureMessage(ctx, run.Namespace, &job)
			if ss.RetryCount < maxRetries {
				logger.Info("step failed, scheduling retry",
					"step", name, "run", run.Name,
					"attempt", ss.RetryCount+1, "maxRetries", maxRetries,
					"reason", failureReason)
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
				logger.Error(fmt.Errorf("%s", failureReason), "step failed permanently",
					"step", name, "run", run.Name, "retries", ss.RetryCount)
				failStepNow(&ss, failureReason)
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
				override := findStepOverride(run.Spec.StepOverrides, stepName)
				var syncErr error
				if override != nil {
					syncErr = r.syncDeployStepFromOverride(ctx, runWithGVK, stepSpec, svcTmpl, override, &ss)
				} else {
					syncErr = r.syncDeployStep(ctx, &chain, &run, stepSpec, svcTmpl, &ss)
				}
				if syncErr != nil {
					logger.Error(syncErr, "deploy step failed to sync", "step", stepName, "run", run.Name)
					failStepNow(&ss, syncErr.Error())
					stepStates[stepName] = dag.StepPhaseFailed
					stepMap[stepName] = ss
					continue
				}
				logger.Info("deploy step starting", "step", stepName, "run", run.Name)
				stepStates[stepName] = dag.StepPhaseRunning
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
						logger.Error(prepErr, "failed to prepare input data", "step", stepName, "run", run.Name)
						failStepNow(&ss, fmt.Sprintf("failed to prepare input data: %v", prepErr))
						stepStates[stepName] = dag.StepPhaseFailed
						stepMap[stepName] = ss
						continue
					}
					if !ready {
						if requeueAfter == 0 || 2*time.Second < requeueAfter {
							requeueAfter = 2 * time.Second
						}
						continue
					}
					inputConfigMap = cmName
				}

				job := jobbuilder.Build(tmpl, stepSpec, &run, ss.RetryCount, inputConfigMap, run.Status.SharedPVCName, r.SecurityDefaults)
				job.OwnerReferences = []metav1.OwnerReference{
					*metav1.NewControllerRef(runWithGVK, weaveRunGVK),
				}
				if err := r.Create(ctx, job); err != nil && !errors.IsAlreadyExists(err) {
					logger.Error(err, "failed to create job", "step", stepName, "run", run.Name)
					failStepNow(&ss, fmt.Sprintf("failed to create job: %v", err))
					stepStates[stepName] = dag.StepPhaseFailed
					stepMap[stepName] = ss
					continue
				}
				logger.Info("job step starting", "step", stepName, "run", run.Name)
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

	// Re-evaluate after applying decisions so newly-started steps (Running/Deployed)
	// are reflected in RunComplete — prevents premature completion on first reconcile.
	advancement = dag.Advance(graph, stepStates, dag.FailurePolicy(chain.Spec.FailurePolicy))

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
			logger.Info("WeaveRun phase transition", "run", run.Name, "phase", "Succeeded")
		} else {
			// Aggregate failed step messages into a run-level summary.
			var parts []string
			for _, ss := range run.Status.Steps {
				if ss.Phase == weavev1alpha1.StepPhaseFailed && ss.Message != "" {
					parts = append(parts, ss.Name+": "+ss.Message)
				}
			}
			if len(parts) > 0 {
				run.Status.Message = strings.Join(parts, "; ")
			}
			if chain.Spec.FailurePolicy == weavev1alpha1.FailurePolicyStopAll {
				run.Status.Phase = weavev1alpha1.RunPhaseStopped
				logger.Info("WeaveRun phase transition", "run", run.Name, "phase", "Stopped", "reason", run.Status.Message)
			} else {
				run.Status.Phase = weavev1alpha1.RunPhaseFailed
				logger.Info("WeaveRun phase transition", "run", run.Name, "phase", "Failed", "reason", run.Status.Message)
			}
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

	// Fetch metadata and version best-effort so WEAVE_* env vars are populated.
	// Failures are non-fatal: the polling loop will correct WEAVE_VERSION on the
	// next successful tag resolution, and env vars will be missing until then.
	var csMeta *indexclient.AppMetadata
	var csVersion string
	if cs := svcTmpl.Spec.CodeSource; cs != nil {
		idxURL := r.resolveIndexURL(cs.IndexURL)
		if m, v, metaErr := indexclient.FetchAppMetadataAndVersion(ctx, idxURL, cs.ArtifactName, cs.Tag); metaErr == nil {
			csMeta, csVersion = m, v
		} else {
			log.FromContext(ctx).Error(metaErr, "fetch metadata for deploy step (best-effort)", "artifact", cs.ArtifactName)
		}
	}

	// Upsert Deployment.
	desired := deploybuilder.Build(svcTmpl, chain.Name, stepSpec.Name, run.Namespace, r.SecurityDefaults, csMeta, csVersion, r.FusionIndexURL, r.LoaderImage, r.WritablePaths)
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

	// Populate code-source tracking fields when the template has a codeSource.
	// Resolve the initial version best-effort; a failure is non-fatal — the
	// polling loop will populate it on the first successful poll.
	if cs := svcTmpl.Spec.CodeSource; cs != nil {
		indexURL := r.resolveIndexURL(cs.IndexURL)
		entry.CodeSourceArtifact = cs.ArtifactName
		entry.CodeSourceTag = cs.Tag
		entry.CodeSourceIndexURL = indexURL
		if version, resolveErr := indexclient.ResolveTag(ctx, indexURL, cs.ArtifactName, cs.Tag); resolveErr == nil {
			entry.CodeSourceDeployedVersion = version
		} else {
			log.FromContext(ctx).Error(resolveErr, "could not resolve initial code-source version",
				"artifact", cs.ArtifactName, "tag", cs.Tag)
		}
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

// handleRestartStep checks for the fusion-platform.io/restart-step annotation on a
// WeaveRun and, if the named step is Deployed, triggers a rolling restart of its
// Deployment by setting the kubectl restartedAt pod-template annotation. The
// annotation is consumed (deleted) after the patch so it acts as a one-shot trigger.
// Returns (true, nil) when a restart was issued; the caller should requeue.
func (r *WeaveRunReconciler) handleRestartStep(ctx context.Context, run *weavev1alpha1.WeaveRun) (bool, error) {
	stepName, ok := run.Annotations[annotationRestartStep]
	if !ok || stepName == "" {
		return false, nil
	}

	// Find the step in the run's status.
	var ss *weavev1alpha1.WeaveRunStepStatus
	for i := range run.Status.Steps {
		if run.Status.Steps[i].Name == stepName {
			ss = &run.Status.Steps[i]
			break
		}
	}

	// Always consume the annotation, even if the step is not restartable.
	metaPatch := client.MergeFrom(run.DeepCopy())
	delete(run.Annotations, annotationRestartStep)
	if err := r.Patch(ctx, run, metaPatch); err != nil {
		return false, fmt.Errorf("remove restart-step annotation: %w", err)
	}

	if ss == nil || ss.Phase != weavev1alpha1.StepPhaseDeployed || ss.DeploymentRef == nil {
		log.FromContext(ctx).Info("restart-step annotation ignored: step not in Deployed phase", "step", stepName)
		return false, nil
	}

	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: ss.DeploymentRef.Name}, &deploy); err != nil {
		return false, fmt.Errorf("get deployment for restart: %w", err)
	}

	deployPatch := client.MergeFrom(deploy.DeepCopy())
	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = map[string]string{}
	}
	deploy.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().UTC().Format(time.RFC3339)
	if err := r.Patch(ctx, &deploy, deployPatch); err != nil {
		return false, fmt.Errorf("patch deployment for restart: %w", err)
	}

	log.FromContext(ctx).Info("rolling restart triggered", "step", stepName, "deployment", ss.DeploymentRef.Name)
	return true, nil
}

// handleRunDeletion is called when the WeaveRun has a non-zero DeletionTimestamp.
// It tears down deploy-step resources (Deployment, Service, Ingress) and removes
// the deploy-cleanup finalizer so Kubernetes GC can complete the deletion.
// Succeeded runs are exempt: their Deployments are preserved for rolling updates
// by future runs on the same chain.
func (r *WeaveRunReconciler) handleRunDeletion(ctx context.Context, run *weavev1alpha1.WeaveRun) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(run, finalizerDeployCleanup) {
		return ctrl.Result{}, nil
	}
	if run.Status.Phase != weavev1alpha1.RunPhaseSucceeded {
		if err := r.doDeployTeardown(ctx, run); err != nil {
			return ctrl.Result{}, err
		}
	}
	controllerutil.RemoveFinalizer(run, finalizerDeployCleanup)
	if err := r.Update(ctx, run); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove deploy-cleanup finalizer on deletion: %w", err)
	}
	return ctrl.Result{}, nil
}

// doDeployTeardown deletes the Deployment, Service, and Ingress for every
// deploy-kind step recorded in the run's status, and removes the corresponding
// entries from WeaveChain.Status.ActiveDeployments. All deletes tolerate
// not-found errors so the function is safe to call multiple times.
func (r *WeaveRunReconciler) doDeployTeardown(ctx context.Context, run *weavev1alpha1.WeaveRun) error {
	logger := log.FromContext(ctx)

	var chain weavev1alpha1.WeaveChain
	chainFound := true
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: run.Namespace, Name: run.Spec.ChainRef.Name,
	}, &chain); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("get chain for deploy teardown: %w", err)
		}
		chainFound = false
	}

	chainPatch := client.MergeFrom(chain.DeepCopy())
	activeDeployChanged := false

	for _, ss := range run.Status.Steps {
		if ss.DeploymentRef == nil {
			continue
		}
		name := ss.DeploymentRef.Name
		ns := run.Namespace

		deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		if err := r.Delete(ctx, deploy); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete deployment %q: %w", name, err)
		}
		logger.Info("deleted deploy-step Deployment", "deployment", name)

		svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		if err := r.Delete(ctx, svc); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete service %q: %w", name, err)
		}

		ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		if err := r.Delete(ctx, ing); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete ingress %q: %w", name, err)
		}

		if chainFound {
			if _, ok := chain.Status.ActiveDeployments[name]; ok {
				delete(chain.Status.ActiveDeployments, name)
				activeDeployChanged = true
			}
		}
	}

	if activeDeployChanged {
		if err := r.Status().Patch(ctx, &chain, chainPatch); err != nil {
			return fmt.Errorf("patch chain status after deploy teardown: %w", err)
		}
	}

	// Clean up run-owned ActiveDeployments entries (step overrides).
	if len(run.Status.ActiveDeployments) > 0 {
		runStatusPatch := client.MergeFrom(run.DeepCopy())
		for _, ss := range run.Status.Steps {
			if ss.DeploymentRef != nil {
				delete(run.Status.ActiveDeployments, ss.DeploymentRef.Name)
			}
		}
		if err := r.Status().Patch(ctx, run, runStatusPatch); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("patch run status after deploy teardown: %w", err)
		}
	}

	return nil
}

// findStepOverride returns the WeaveRunStepOverride for stepName, or nil if none.
func findStepOverride(overrides []weavev1alpha1.WeaveRunStepOverride, stepName string) *weavev1alpha1.WeaveRunStepOverride {
	for i := range overrides {
		if overrides[i].StepName == stepName {
			return &overrides[i]
		}
	}
	return nil
}

// syncDeployStepFromOverride creates or rolling-updates the run-owned Deployment,
// Service, and Ingress for a deploy step with a WeaveRunStepOverride. The resources
// are named <runName>-<stepName> and owned by the WeaveRun. AppMetadata is fetched
// from fusion-index at sync time to apply runner port, args, and resource limits.
func (r *WeaveRunReconciler) syncDeployStepFromOverride(
	ctx context.Context,
	runWithGVK *weavev1alpha1.WeaveRun,
	stepSpec *weavev1alpha1.WeaveChainStep,
	svcTmpl *weavev1alpha1.WeaveServiceTemplate,
	override *weavev1alpha1.WeaveRunStepOverride,
	ss *weavev1alpha1.WeaveRunStepStatus,
) error {
	indexURL := r.resolveIndexURL(override.IndexURL)
	meta, csVersion, err := indexclient.FetchAppMetadataAndVersion(ctx, indexURL, override.ArtifactName, override.Tag)
	if err != nil {
		return fmt.Errorf("fetch app metadata for %s@%s: %w", override.ArtifactName, override.Tag, err)
	}

	ownerRef := metav1.NewControllerRef(runWithGVK, weaveRunGVK)
	deployName := deploybuilder.RunDeploymentName(runWithGVK.Name, stepSpec.Name)

	desired := deploybuilder.BuildFromOverride(svcTmpl, override, meta, runWithGVK.Name, stepSpec.Name, runWithGVK.Namespace, r.SecurityDefaults, csVersion, r.FusionIndexURL, r.LoaderImage, r.WritablePaths)
	desired.OwnerReferences = []metav1.OwnerReference{*ownerRef}

	var existing appsv1.Deployment
	err = r.Get(ctx, types.NamespacedName{Namespace: runWithGVK.Namespace, Name: deployName}, &existing)
	if errors.IsNotFound(err) {
		if createErr := r.Create(ctx, desired); createErr != nil && !errors.IsAlreadyExists(createErr) {
			return fmt.Errorf("create override deployment %q: %w", deployName, createErr)
		}
	} else if err == nil {
		patch := client.MergeFrom(existing.DeepCopy())
		existing.Spec.Template = desired.Spec.Template
		existing.Spec.Replicas = desired.Spec.Replicas
		if patchErr := r.Patch(ctx, &existing, patch); patchErr != nil {
			return fmt.Errorf("patch override deployment %q: %w", deployName, patchErr)
		}
	} else {
		return fmt.Errorf("get override deployment %q: %w", deployName, err)
	}

	svc := deploybuilder.BuildServiceFromOverride(svcTmpl, meta, runWithGVK.Name, stepSpec.Name, runWithGVK.Namespace)
	svc.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	if err := r.upsertService(ctx, svc); err != nil {
		return fmt.Errorf("upsert override service %q: %w", svc.Name, err)
	}

	ing := deploybuilder.BuildIngressFromOverride(svcTmpl, meta, override, runWithGVK.Name, stepSpec.Name, runWithGVK.Namespace)
	if ing != nil {
		ing.OwnerReferences = []metav1.OwnerReference{*ownerRef}
		if err := r.upsertIngress(ctx, ing); err != nil {
			return fmt.Errorf("upsert override ingress %q: %w", ing.Name, err)
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

// registerRunActiveDeployment records a run-owned deployment in run.Status.ActiveDeployments
// for code-source polling. It mutates run in-place; the caller's final status patch persists it.
func (r *WeaveRunReconciler) registerRunActiveDeployment(
	run *weavev1alpha1.WeaveRun,
	stepName, deploymentName string,
	override *weavev1alpha1.WeaveRunStepOverride,
	svcTmpl *weavev1alpha1.WeaveServiceTemplate,
) {
	dur, err := time.ParseDuration(svcTmpl.Spec.UnhealthyDuration)
	if err != nil {
		dur = 5 * time.Minute
	}
	indexURL := r.resolveIndexURL(override.IndexURL)
	entry := weavev1alpha1.WeaveActiveDeploymentStatus{
		DeploymentName:           deploymentName,
		StepName:                 stepName,
		Health:                   weavev1alpha1.DeployHealthHealthy,
		UnhealthyDurationSeconds: int64(dur.Seconds()),
		CodeSourceArtifact:       override.ArtifactName,
		CodeSourceTag:            override.Tag,
		CodeSourceIndexURL:       indexURL,
	}
	if run.Status.ActiveDeployments == nil {
		run.Status.ActiveDeployments = map[string]weavev1alpha1.WeaveActiveDeploymentStatus{}
	}
	run.Status.ActiveDeployments[deploymentName] = entry
}

// pollRunDeploymentCodeSource checks if the tracked artifact tag has moved to a new
// version and triggers a rolling restart when it has. Mutates run.Status.ActiveDeployments
// in-place; the caller's final status patch persists the updated CodeSourceDeployedVersion.
func (r *WeaveRunReconciler) pollRunDeploymentCodeSource(ctx context.Context, run *weavev1alpha1.WeaveRun, deploymentName string) error {
	entry, ok := run.Status.ActiveDeployments[deploymentName]
	if !ok || entry.CodeSourceArtifact == "" {
		return nil
	}
	current, err := indexclient.ResolveTag(ctx, entry.CodeSourceIndexURL, entry.CodeSourceArtifact, entry.CodeSourceTag)
	if err != nil {
		return fmt.Errorf("resolve tag: %w", err)
	}
	if current == "" || current == entry.CodeSourceDeployedVersion {
		return nil
	}

	log.FromContext(ctx).Info("code-source version changed, triggering rolling restart",
		"artifact", entry.CodeSourceArtifact,
		"old", entry.CodeSourceDeployedVersion,
		"new", current)

	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: deploymentName}, &deploy); err != nil {
		return fmt.Errorf("get deployment for code reload: %w", err)
	}
	deployPatch := client.MergeFrom(deploy.DeepCopy())
	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = map[string]string{}
	}
	deploy.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().UTC().Format(time.RFC3339)
	deploy.Spec.Template.Annotations["fusion-platform.io/code-source-version"] = current
	deploybuilder.UpdateVersionEnvVar(deploy.Spec.Template.Spec.Containers, current)
	if err := r.Patch(ctx, &deploy, deployPatch); err != nil {
		return fmt.Errorf("patch deployment for code reload: %w", err)
	}

	entry.CodeSourceDeployedVersion = current
	run.Status.ActiveDeployments[deploymentName] = entry
	return nil
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
					(ss.Phase == weavev1alpha1.StepPhaseRunning || ss.Phase == weavev1alpha1.StepPhaseDeployed) {
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
