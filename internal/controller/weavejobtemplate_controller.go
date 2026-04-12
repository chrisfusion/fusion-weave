// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

// WeaveJobTemplateReconciler validates WeaveJobTemplate specs and sets status.valid.
type WeaveJobTemplateReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=fluxjobtemplates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=fluxjobtemplates/status,verbs=get;update;patch

func (r *WeaveJobTemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var tmpl weavev1alpha1.WeaveJobTemplate
	if err := r.Get(ctx, req.NamespacedName, &tmpl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	valid, msg := validateJobTemplate(&tmpl.Spec)
	if tmpl.Status.Valid == valid &&
		tmpl.Status.ValidationMessage == msg &&
		tmpl.Status.ObservedGeneration == tmpl.Generation {
		return ctrl.Result{}, nil
	}

	patch := client.MergeFrom(tmpl.DeepCopy())
	tmpl.Status.Valid = valid
	tmpl.Status.ValidationMessage = msg
	tmpl.Status.ObservedGeneration = tmpl.Generation

	if err := r.Status().Patch(ctx, &tmpl, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch status: %w", err)
	}

	if valid {
		logger.Info("WeaveJobTemplate is valid")
	} else {
		logger.Info("WeaveJobTemplate is invalid", "reason", msg)
	}
	return ctrl.Result{}, nil
}

func validateJobTemplate(spec *weavev1alpha1.WeaveJobTemplateSpec) (bool, string) {
	if spec.Image == "" {
		return false, "spec.image is required"
	}
	for _, vm := range spec.Volumes {
		if vm.SecretName == "" && vm.ConfigMapName == "" {
			return false, fmt.Sprintf("volume %q must set secretName or configMapName", vm.Name)
		}
		if vm.SecretName != "" && vm.ConfigMapName != "" {
			return false, fmt.Sprintf("volume %q must set only one of secretName or configMapName", vm.Name)
		}
		if vm.MountPath == "" {
			return false, fmt.Sprintf("volume %q must set mountPath", vm.Name)
		}
	}
	return true, ""
}

func (r *WeaveJobTemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&weavev1alpha1.WeaveJobTemplate{}).
		Complete(r)
}
