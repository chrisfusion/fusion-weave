// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

// WeaveServiceTemplateReconciler validates WeaveServiceTemplate specs and sets status.valid.
type WeaveServiceTemplateReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=weaveservicetemplates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=weaveservicetemplates/status,verbs=get;update;patch

func (r *WeaveServiceTemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var tmpl weavev1alpha1.WeaveServiceTemplate
	if err := r.Get(ctx, req.NamespacedName, &tmpl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	valid, msg := validateServiceTemplate(&tmpl.Spec)
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
		logger.Info("WeaveServiceTemplate is valid")
	} else {
		logger.Info("WeaveServiceTemplate is invalid", "reason", msg)
	}
	return ctrl.Result{}, nil
}

func validateServiceTemplate(spec *weavev1alpha1.WeaveServiceTemplateSpec) (bool, string) {
	if spec.Image == "" {
		return false, "spec.image is required"
	}
	if len(spec.Ports) == 0 {
		return false, "spec.ports must contain at least one port"
	}
	portNames := map[string]bool{}
	for _, p := range spec.Ports {
		if p.Port <= 0 {
			return false, fmt.Sprintf("port %q: port number must be > 0", p.Name)
		}
		portNames[p.Name] = true
	}
	if _, err := time.ParseDuration(spec.UnhealthyDuration); err != nil {
		return false, fmt.Sprintf("spec.unhealthyDuration %q is not a valid duration: %v", spec.UnhealthyDuration, err)
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
	if spec.Ingress != nil {
		if len(spec.Ingress.Rules) == 0 {
			return false, "spec.ingress.rules must contain at least one rule"
		}
		for _, rule := range spec.Ingress.Rules {
			if rule.Host == "" {
				return false, "ingress rule must have a non-empty host"
			}
			if !portNames[rule.ServicePort] {
				return false, fmt.Sprintf("ingress rule host %q references servicePort %q which is not declared in spec.ports", rule.Host, rule.ServicePort)
			}
		}
	}
	return true, ""
}

func (r *WeaveServiceTemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&weavev1alpha1.WeaveServiceTemplate{}).
		Complete(r)
}
