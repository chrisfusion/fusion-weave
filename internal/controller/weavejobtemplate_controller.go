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

	tmpl.Status.Valid = valid
	tmpl.Status.ValidationMessage = msg
	tmpl.Status.ObservedGeneration = tmpl.Generation

	// Status.Valid has no +optional marker, so on a brand-new template (invalid
	// from its very first reconcile) a MergeFrom-diffed patch that happens not to
	// change Valid's value — its zero value (false) already equals "new" when the
	// template is invalid — would omit the required field entirely and get
	// rejected with "status.valid: Required value", permanently deadlocking this
	// template's reconcile in that same error forever. Same root cause as the
	// WeaveTrigger.Status.Active incident fixed in 2baadb2. Update() always sends
	// the full status, so it can't hit that gap.
	if err := r.Status().Update(ctx, &tmpl); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	if valid {
		logger.Info("WeaveJobTemplate is valid", "template", tmpl.Name)
	} else {
		logger.Info("WeaveJobTemplate is invalid", "template", tmpl.Name, "reason", msg)
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
