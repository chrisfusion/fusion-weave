// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/dag"
	"fusion-platform.io/fusion-weave/internal/deploybuilder"
)

// weaveChainGVK is used by WeaveRunReconciler to set owner references on
// Deployments created for deploy-kind steps. Owner must be WeaveChain (not
// WeaveRun) so the Deployment survives run deletion.
var weaveChainGVK = schema.GroupVersionKind{
	Group:   "weave.fusion-platform.io",
	Version: "v1alpha1",
	Kind:    "WeaveChain",
}

// WeaveChainReconciler validates WeaveChain DAG topology and referenced templates.
type WeaveChainReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=weavechains,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=weavechains/status,verbs=get;update;patch

func (r *WeaveChainReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var chain weavev1alpha1.WeaveChain
	if err := r.Get(ctx, req.NamespacedName, &chain); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Capture patch base before any mutations.
	patch := client.MergeFrom(chain.DeepCopy())

	valid, msg := r.validateChain(ctx, &chain)

	validationChanged := chain.Status.Valid != valid ||
		chain.Status.ValidationMessage != msg ||
		chain.Status.ObservedGeneration != chain.Generation

	chain.Status.Valid = valid
	chain.Status.ValidationMessage = msg
	chain.Status.ObservedGeneration = chain.Generation

	requeueAfter, healthChanged := r.syncDeploymentHealth(ctx, &chain)

	if validationChanged || healthChanged {
		if err := r.Status().Patch(ctx, &chain, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch status: %w", err)
		}
	}

	if valid {
		logger.Info("WeaveChain is valid")
	} else {
		logger.Info("WeaveChain is invalid", "reason", msg)
	}

	if requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

func (r *WeaveChainReconciler) validateChain(ctx context.Context, chain *weavev1alpha1.WeaveChain) (bool, string) {
	// Verify all referenced templates exist and are valid.
	for _, step := range chain.Spec.Steps {
		kind := step.StepKind
		if kind == "" {
			kind = weavev1alpha1.StepKindJob
		}

		switch kind {
		case weavev1alpha1.StepKindJob:
			if step.JobTemplateRef == nil {
				return false, fmt.Sprintf("step %q: jobTemplateRef is required for Job-kind steps", step.Name)
			}
			var tmpl weavev1alpha1.WeaveJobTemplate
			if err := r.Get(ctx, types.NamespacedName{
				Namespace: chain.Namespace,
				Name:      step.JobTemplateRef.Name,
			}, &tmpl); err != nil {
				return false, fmt.Sprintf("step %q: template %q not found", step.Name, step.JobTemplateRef.Name)
			}
			if !tmpl.Status.Valid {
				return false, fmt.Sprintf("step %q: template %q is not valid: %s",
					step.Name, step.JobTemplateRef.Name, tmpl.Status.ValidationMessage)
			}

		case weavev1alpha1.StepKindDeploy:
			if step.ServiceTemplateRef == nil {
				return false, fmt.Sprintf("step %q: serviceTemplateRef is required for Deploy-kind steps", step.Name)
			}
			var tmpl weavev1alpha1.WeaveServiceTemplate
			if err := r.Get(ctx, types.NamespacedName{
				Namespace: chain.Namespace,
				Name:      step.ServiceTemplateRef.Name,
			}, &tmpl); err != nil {
				return false, fmt.Sprintf("step %q: service template %q not found", step.Name, step.ServiceTemplateRef.Name)
			}
			if !tmpl.Status.Valid {
				return false, fmt.Sprintf("step %q: service template %q is not valid: %s",
					step.Name, step.ServiceTemplateRef.Name, tmpl.Status.ValidationMessage)
			}
		}
	}

	// Build the DAG to detect cycles and unknown deps.
	nodes := make([]dag.Node, len(chain.Spec.Steps))
	for i, step := range chain.Spec.Steps {
		nodes[i] = dag.Node{
			Name:         step.Name,
			DependsOn:    step.DependsOn,
			RunOnSuccess: step.RunOnSuccess,
			RunOnFailure: step.RunOnFailure,
		}
	}
	graph, err := dag.BuildGraph(nodes)
	if err != nil {
		return false, err.Error()
	}

	// Build a quick lookup of which steps produce output.
	produces := make(map[string]bool, len(chain.Spec.Steps))
	for _, step := range chain.Spec.Steps {
		if step.ProducesOutput {
			produces[step.Name] = true
		}
	}

	// Validate ConsumesOutputFrom declarations.
	for _, step := range chain.Spec.Steps {
		if len(step.ConsumesOutputFrom) == 0 {
			continue
		}
		ancestors := graph.Ancestors(step.Name)
		for _, producer := range step.ConsumesOutputFrom {
			if graph.Node(producer) == nil {
				return false, fmt.Sprintf("step %q: consumesOutputFrom references unknown step %q", step.Name, producer)
			}
			if !produces[producer] {
				return false, fmt.Sprintf("step %q: consumesOutputFrom references step %q which does not have producesOutput: true", step.Name, producer)
			}
			if !ancestors[producer] {
				return false, fmt.Sprintf("step %q: consumesOutputFrom references step %q which is not a dependency of this step", step.Name, producer)
			}
		}
	}

	// Validate the shared storage size is a parseable resource quantity.
	if chain.Spec.SharedStorage != nil {
		if _, err := resource.ParseQuantity(chain.Spec.SharedStorage.Size); err != nil {
			return false, fmt.Sprintf("sharedStorage.size %q is not a valid resource quantity: %v", chain.Spec.SharedStorage.Size, err)
		}
	}

	return true, ""
}

// syncDeploymentHealth checks all active Deployments registered in the chain
// status and triggers rollback when a Deployment remains unhealthy beyond its
// configured threshold. Returns whether any status field changed.
func (r *WeaveChainReconciler) syncDeploymentHealth(ctx context.Context, chain *weavev1alpha1.WeaveChain) (time.Duration, bool) {
	logger := log.FromContext(ctx)
	if len(chain.Status.ActiveDeployments) == 0 {
		return 0, false
	}

	changed := false
	var minRequeue time.Duration

	for key, entry := range chain.Status.ActiveDeployments {
		var deploy appsv1.Deployment
		err := r.Get(ctx, types.NamespacedName{
			Namespace: chain.Namespace,
			Name:      entry.DeploymentName,
		}, &deploy)
		if err != nil {
			if client.IgnoreNotFound(err) != nil {
				logger.Error(err, "get deployment for health check", "deployment", entry.DeploymentName)
			}
			if entry.Health != weavev1alpha1.DeployHealthUnknown {
				entry.Health = weavev1alpha1.DeployHealthUnknown
				entry.Message = "deployment not found or error"
				chain.Status.ActiveDeployments[key] = entry
				changed = true
			}
			continue
		}

		available := isDeploymentAvailable(&deploy)

		if available {
			if entry.Health != weavev1alpha1.DeployHealthHealthy {
				entry.Health = weavev1alpha1.DeployHealthHealthy
				entry.UnhealthySince = nil
				entry.Message = ""
				rev := deploy.Annotations["deployment.kubernetes.io/revision"]
				if rev != "" {
					entry.CurrentRevision = rev
				}
				chain.Status.ActiveDeployments[key] = entry
				changed = true
			}
			continue
		}

		// Deployment is not available.
		switch entry.Health {
		case weavev1alpha1.DeployHealthHealthy, weavev1alpha1.DeployHealthUnknown, weavev1alpha1.DeployHealthRolledBack:
			// Transition to Unhealthy.
			now := metav1.Now()
			entry.Health = weavev1alpha1.DeployHealthUnhealthy
			entry.UnhealthySince = &now
			entry.Message = "deployment is not available"
			chain.Status.ActiveDeployments[key] = entry
			changed = true

		case weavev1alpha1.DeployHealthUnhealthy:
			// Check if we've exceeded the threshold.
			if entry.UnhealthySince != nil && entry.UnhealthyDurationSeconds > 0 {
				threshold := time.Duration(entry.UnhealthyDurationSeconds) * time.Second
				elapsed := time.Since(entry.UnhealthySince.Time)
				if elapsed >= threshold {
					logger.Info("triggering rollback", "deployment", entry.DeploymentName, "elapsed", elapsed)
					if err := r.rollbackDeployment(ctx, chain.Namespace, &entry, &deploy); err != nil {
						logger.Error(err, "rollback failed", "deployment", entry.DeploymentName)
						entry.Message = fmt.Sprintf("rollback failed: %v", err)
					} else {
						entry.Health = weavev1alpha1.DeployHealthRollingBack
						now := metav1.Now()
						entry.LastRollbackTime = &now
						entry.Message = "rollback triggered"
					}
					chain.Status.ActiveDeployments[key] = entry
					changed = true
				} else {
					// Requeue when the threshold expires.
					remaining := threshold - elapsed
					if minRequeue == 0 || remaining < minRequeue {
						minRequeue = remaining
					}
				}
			}

		case weavev1alpha1.DeployHealthRollingBack:
			// Still rolling back — nothing to do, wait for Available to flip.
		}
	}

	return minRequeue, changed
}

// rollbackDeployment finds the previous ReplicaSet revision and patches the
// Deployment's pod template back to that revision's template.
func (r *WeaveChainReconciler) rollbackDeployment(
	ctx context.Context,
	namespace string,
	entry *weavev1alpha1.WeaveActiveDeploymentStatus,
	deploy *appsv1.Deployment,
) error {
	currentRevStr := deploy.Annotations["deployment.kubernetes.io/revision"]
	currentRev, err := strconv.ParseInt(currentRevStr, 10, 64)
	if err != nil || currentRev <= 1 {
		return fmt.Errorf("cannot roll back: current revision %q is not rollback-eligible", currentRevStr)
	}
	targetRev := currentRev - 1

	// List all ReplicaSets for this deployment (matched by selector labels).
	var rsList appsv1.ReplicaSetList
	if err := r.List(ctx, &rsList,
		client.InNamespace(namespace),
		client.MatchingLabels{
			deploybuilder.ChainLabel: deploy.Labels[deploybuilder.ChainLabel],
			deploybuilder.StepLabel:  deploy.Labels[deploybuilder.StepLabel],
		},
	); err != nil {
		return fmt.Errorf("list replicasets: %w", err)
	}

	// Find the RS at the target revision.
	sort.Slice(rsList.Items, func(i, j int) bool {
		ri, _ := strconv.ParseInt(rsList.Items[i].Annotations["deployment.kubernetes.io/revision"], 10, 64)
		rj, _ := strconv.ParseInt(rsList.Items[j].Annotations["deployment.kubernetes.io/revision"], 10, 64)
		return ri < rj
	})

	var targetRS *appsv1.ReplicaSet
	for i := range rsList.Items {
		rev, _ := strconv.ParseInt(rsList.Items[i].Annotations["deployment.kubernetes.io/revision"], 10, 64)
		if rev == targetRev {
			targetRS = &rsList.Items[i]
			break
		}
	}
	if targetRS == nil {
		return fmt.Errorf("revision %d not found in replicaset history", targetRev)
	}

	// Patch the Deployment's pod template from the target RS.
	patchBase := deploy.DeepCopy()
	deploy.Spec.Template = targetRS.Spec.Template
	if err := r.Patch(ctx, deploy, client.MergeFrom(patchBase)); err != nil {
		return fmt.Errorf("patch deployment: %w", err)
	}

	entry.LastRollbackRevision = strconv.FormatInt(targetRev, 10)
	return nil
}

func (r *WeaveChainReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Watch WeaveJobTemplate changes and enqueue any WeaveChain that references them.
	enqueueByJobTemplate := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		tmpl, ok := obj.(*weavev1alpha1.WeaveJobTemplate)
		if !ok {
			return nil
		}
		var chainList weavev1alpha1.WeaveChainList
		if err := r.List(ctx, &chainList, client.InNamespace(tmpl.Namespace)); err != nil {
			return nil
		}
		var reqs []reconcile.Request
		for _, chain := range chainList.Items {
			for _, step := range chain.Spec.Steps {
				if step.JobTemplateRef != nil && step.JobTemplateRef.Name == tmpl.Name {
					reqs = append(reqs, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Namespace: chain.Namespace,
							Name:      chain.Name,
						},
					})
					break
				}
			}
		}
		return reqs
	})

	// Watch WeaveServiceTemplate changes and enqueue any WeaveChain that references them.
	enqueueByServiceTemplate := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		tmpl, ok := obj.(*weavev1alpha1.WeaveServiceTemplate)
		if !ok {
			return nil
		}
		var chainList weavev1alpha1.WeaveChainList
		if err := r.List(ctx, &chainList, client.InNamespace(tmpl.Namespace)); err != nil {
			return nil
		}
		var reqs []reconcile.Request
		for _, chain := range chainList.Items {
			for _, step := range chain.Spec.Steps {
				if step.ServiceTemplateRef != nil && step.ServiceTemplateRef.Name == tmpl.Name {
					reqs = append(reqs, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Namespace: chain.Namespace,
							Name:      chain.Name,
						},
					})
					break
				}
			}
		}
		return reqs
	})

	// Watch Deployment changes and enqueue the owning WeaveChain by label.
	enqueueByDeployment := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		chainName := obj.GetLabels()[deploybuilder.ChainLabel]
		if chainName == "" {
			return nil
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Namespace: obj.GetNamespace(),
				Name:      chainName,
			},
		}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&weavev1alpha1.WeaveChain{}).
		Watches(&weavev1alpha1.WeaveJobTemplate{}, enqueueByJobTemplate).
		Watches(&weavev1alpha1.WeaveServiceTemplate{}, enqueueByServiceTemplate).
		Watches(&appsv1.Deployment{}, enqueueByDeployment).
		Complete(r)
}
