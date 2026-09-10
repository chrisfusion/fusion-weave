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

	// IngressHostSuffix is the cluster-wide domain suffix (ingress.hostSuffix
	// Helm value / INGRESS_HOST_SUFFIX env var) appended to every ingress
	// rule's Name to form the full hostname. A template with an Ingress spec
	// is invalid until this is configured.
	IngressHostSuffix string
}

// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=weaveservicetemplates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=weaveservicetemplates/status,verbs=get;update;patch

func (r *WeaveServiceTemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var tmpl weavev1alpha1.WeaveServiceTemplate
	if err := r.Get(ctx, req.NamespacedName, &tmpl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	valid, msg := validateServiceTemplate(&tmpl.Spec, r.IngressHostSuffix)
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
		logger.Info("WeaveServiceTemplate is valid", "template", tmpl.Name)
	} else {
		logger.Info("WeaveServiceTemplate is invalid", "template", tmpl.Name, "reason", msg)
	}
	return ctrl.Result{}, nil
}

func validateServiceTemplate(spec *weavev1alpha1.WeaveServiceTemplateSpec, ingressHostSuffix string) (bool, string) {
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
		if ingressHostSuffix == "" {
			return false, "operator ingress host suffix is not configured; set ingress.hostSuffix in the Helm chart (INGRESS_HOST_SUFFIX)"
		}
		if len(spec.Ingress.Rules) == 0 {
			return false, "spec.ingress.rules must contain at least one rule"
		}
		for _, rule := range spec.Ingress.Rules {
			if rule.Name == "" {
				return false, "ingress rule must have a non-empty name"
			}
			if !portNames[rule.ServicePort] {
				return false, fmt.Sprintf("ingress rule name %q references servicePort %q which is not declared in spec.ports", rule.Name, rule.ServicePort)
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
