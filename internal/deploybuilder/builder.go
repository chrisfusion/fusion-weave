// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package deploybuilder

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

// ChainLabel and StepLabel are the immutable selector labels applied to
// Deployment pods. They must never include the run name so that rolling
// updates across runs target the same ReplicaSet selector.
const (
	ChainLabel = "fusion-platform.io/chain"
	StepLabel  = "fusion-platform.io/step"
)

// Build constructs an apps/v1 Deployment for a deploy-kind step.
// The Deployment is owned by the WeaveChain (not the WeaveRun) so it persists
// across runs. The caller must set the OwnerReference.
func Build(
	tmpl *weavev1alpha1.WeaveServiceTemplate,
	chainName, stepName, namespace string,
) *appsv1.Deployment {
	name := DeploymentName(chainName, stepName)
	labels := map[string]string{
		ChainLabel: chainName,
		StepLabel:  stepName,
	}

	replicas := tmpl.Spec.Replicas
	if replicas == 0 {
		replicas = 1
	}

	revLimit := tmpl.Spec.RevisionHistoryLimit

	volumes, mounts := buildVolumes(tmpl.Spec.Volumes)

	container := corev1.Container{
		Name:           "service",
		Image:          tmpl.Spec.Image,
		Command:        tmpl.Spec.Command,
		Args:           tmpl.Spec.Args,
		Env:            tmpl.Spec.Env,
		Resources:      tmpl.Spec.Resources,
		VolumeMounts:   mounts,
		LivenessProbe:  tmpl.Spec.LivenessProbe,
		ReadinessProbe: tmpl.Spec.ReadinessProbe,
		StartupProbe:   tmpl.Spec.StartupProbe,
	}

	for _, p := range tmpl.Spec.Ports {
		container.Ports = append(container.Ports, corev1.ContainerPort{
			Name:          p.Name,
			ContainerPort: effectiveTargetPort(p),
			Protocol:      p.Protocol,
		})
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas:             &replicas,
			RevisionHistoryLimit: revLimit,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: tmpl.Spec.ServiceAccountName,
					Containers:         []corev1.Container{container},
					Volumes:            volumes,
				},
			},
		},
	}
}

// BuildService constructs a corev1.Service for a deploy-kind step.
func BuildService(
	tmpl *weavev1alpha1.WeaveServiceTemplate,
	chainName, stepName, namespace string,
) *corev1.Service {
	name := ServiceName(chainName, stepName)
	labels := map[string]string{
		ChainLabel: chainName,
		StepLabel:  stepName,
	}

	svcType := tmpl.Spec.ServiceType
	if svcType == "" {
		svcType = corev1.ServiceTypeClusterIP
	}

	var ports []corev1.ServicePort
	for _, p := range tmpl.Spec.Ports {
		ports = append(ports, corev1.ServicePort{
			Name:       p.Name,
			Port:       p.Port,
			TargetPort: intstr.FromInt32(effectiveTargetPort(p)),
			Protocol:   p.Protocol,
		})
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: labels,
			Ports:    ports,
		},
	}
}

// BuildIngress constructs a networkingv1.Ingress for a deploy-kind step.
// Returns nil when the template has no Ingress spec.
func BuildIngress(
	tmpl *weavev1alpha1.WeaveServiceTemplate,
	chainName, stepName, namespace string,
) *networkingv1.Ingress {
	if tmpl.Spec.Ingress == nil {
		return nil
	}

	name := IngressName(chainName, stepName)
	svcName := ServiceName(chainName, stepName)
	spec := tmpl.Spec.Ingress
	labels := map[string]string{
		ChainLabel: chainName,
		StepLabel:  stepName,
	}

	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
	}

	if spec.IngressClassName != nil {
		ing.Spec.IngressClassName = spec.IngressClassName
	}

	for _, rule := range spec.Rules {
		pathType := networkingv1.PathTypePrefix
		if rule.PathType == "Exact" {
			pathType = networkingv1.PathTypeExact
		} else if rule.PathType == "ImplementationSpecific" {
			pathType = networkingv1.PathTypeImplementationSpecific
		}

		path := rule.Path
		if path == "" {
			path = "/"
		}

		// Resolve servicePort: try name first, then numeric.
		backend := networkingv1.IngressBackend{
			Service: &networkingv1.IngressServiceBackend{
				Name: svcName,
				Port: resolveServicePort(rule.ServicePort, tmpl.Spec.Ports),
			},
		}

		ing.Spec.Rules = append(ing.Spec.Rules, networkingv1.IngressRule{
			Host: rule.Host,
			IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{
						{
							Path:     path,
							PathType: &pathType,
							Backend:  backend,
						},
					},
				},
			},
		})
	}

	if spec.TLSSecretName != "" {
		var hosts []string
		for _, r := range spec.Rules {
			hosts = append(hosts, r.Host)
		}
		ing.Spec.TLS = []networkingv1.IngressTLS{
			{
				Hosts:      hosts,
				SecretName: spec.TLSSecretName,
			},
		}
	}

	return ing
}

// resolveServicePort finds the ServicePort by name (string) or returns a
// numeric port when the name looks like a number.
func resolveServicePort(ref string, ports []weavev1alpha1.WeaveServicePort) networkingv1.ServiceBackendPort {
	for _, p := range ports {
		if p.Name == ref {
			return networkingv1.ServiceBackendPort{Name: p.Name}
		}
	}
	// Fall back to treating ref as a numeric port string.
	var num int32
	if _, err := fmt.Sscanf(ref, "%d", &num); err == nil {
		return networkingv1.ServiceBackendPort{Number: num}
	}
	return networkingv1.ServiceBackendPort{Name: ref}
}

// effectiveTargetPort returns the container port for a WeaveServicePort.
// When TargetPort is 0 (omitted), it falls back to Port.
func effectiveTargetPort(p weavev1alpha1.WeaveServicePort) int32 {
	if p.TargetPort > 0 {
		return p.TargetPort
	}
	return p.Port
}

// buildVolumes converts WeaveVolumeMount declarations into pod Volumes and
// container VolumeMounts. Mirrors the same function in jobbuilder.
func buildVolumes(mounts []weavev1alpha1.WeaveVolumeMount) ([]corev1.Volume, []corev1.VolumeMount) {
	volumes := make([]corev1.Volume, 0, len(mounts))
	volumeMounts := make([]corev1.VolumeMount, 0, len(mounts))

	for _, m := range mounts {
		var source corev1.VolumeSource
		if m.SecretName != "" {
			source = corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: m.SecretName},
			}
		} else if m.ConfigMapName != "" {
			source = corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: m.ConfigMapName},
				},
			}
		}
		volumes = append(volumes, corev1.Volume{
			Name:         m.Name,
			VolumeSource: source,
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      m.Name,
			MountPath: m.MountPath,
		})
	}
	return volumes, volumeMounts
}
