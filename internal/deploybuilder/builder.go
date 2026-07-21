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
	"fusion-platform.io/fusion-weave/internal/codesource"
	"fusion-platform.io/fusion-weave/internal/indexclient"
	"fusion-platform.io/fusion-weave/internal/security"
)

// ChainLabel and StepLabel are the immutable selector labels applied to
// Deployment pods. They must never include the run name so that rolling
// updates across runs target the same ReplicaSet selector.
const (
	ChainLabel = "fusion-platform.io/chain"
	StepLabel  = "fusion-platform.io/step"
)

// UpdateVersionEnvVar replaces WEAVE_VERSION on every container in the slice,
// or appends it when not present. Call this alongside the restartedAt annotation
// patch in triggerCodeReload so new pods see the correct version immediately.
func UpdateVersionEnvVar(containers []corev1.Container, version string) {
	for i := range containers {
		found := false
		for j := range containers[i].Env {
			if containers[i].Env[j].Name == "WEAVE_VERSION" {
				containers[i].Env[j].Value = version
				found = true
				break
			}
		}
		if !found {
			containers[i].Env = append(containers[i].Env, corev1.EnvVar{Name: "WEAVE_VERSION", Value: version})
		}
	}
}

// Build constructs an apps/v1 Deployment for a deploy-kind step.
// The Deployment is owned by the WeaveChain (not the WeaveRun) so it persists
// across runs. The caller must set the OwnerReference.
// sec carries operator-wide security defaults applied to the pod template and
// all containers (including the code-loader init container when present).
func Build(
	tmpl *weavev1alpha1.WeaveServiceTemplate,
	chainName, stepName, namespace string,
	sec security.Defaults,
	meta *indexclient.AppMetadata,
	version string,
	defaultIndexURL string,
	defaultLoaderImage string,
	writablePaths []string,
	authSecretName string,
) *appsv1.Deployment {
	name := DeploymentName(chainName, stepName)
	labels := map[string]string{
		ChainLabel: chainName,
		StepLabel:  stepName,
	}

	// Resolve effective security contexts: template fields override global defaults.
	podSC := sec.PodSecurityContext
	if tmpl.Spec.PodSecurityContext != nil {
		podSC = tmpl.Spec.PodSecurityContext
	}
	containerSC := sec.ContainerSecurityContext
	if tmpl.Spec.ContainerSecurityContext != nil {
		containerSC = tmpl.Spec.ContainerSecurityContext
	}

	replicas := tmpl.Spec.Replicas
	if replicas == 0 {
		replicas = 1
	}

	revLimit := tmpl.Spec.RevisionHistoryLimit

	volumes, mounts := buildVolumes(tmpl.Spec.Volumes)

	var initContainers []corev1.Container
	if cs := tmpl.Spec.CodeSource; cs != nil {
		mountPath := cs.MountPath
		if mountPath == "" {
			mountPath = "/weave-code"
		}
		indexURL := cs.IndexURL
		if indexURL == "" {
			indexURL = defaultIndexURL
		}
		if indexURL == "" {
			indexURL = "http://fusion-index-backend.fusion.svc.cluster.local:8080"
		}
		loaderImage := cs.LoaderImage
		if loaderImage == "" {
			loaderImage = defaultLoaderImage
		}
		if loaderImage == "" {
			loaderImage = "fusion-code-loader:latest"
		}
		volumes = append(volumes, corev1.Volume{
			Name:         "weave-code",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
		mounts = append(mounts, corev1.VolumeMount{Name: "weave-code", MountPath: mountPath})
		initMounts := []corev1.VolumeMount{{Name: "weave-code", MountPath: mountPath}}
		for _, p := range writablePaths {
			volName := codesource.WritableVolumeName(p)
			if volName == "" || codesource.HasVolume(volumes, volName) {
				continue
			}
			volumes = append(volumes, corev1.Volume{
				Name:         volName,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			})
			vm := corev1.VolumeMount{Name: volName, MountPath: p}
			mounts = append(mounts, vm)
			initMounts = append(initMounts, vm)
		}
		loaderPullPolicy := cs.LoaderImagePullPolicy
		if loaderPullPolicy == "" {
			loaderPullPolicy = corev1.PullIfNotPresent
		}
		initContainers = append(initContainers, corev1.Container{
			Name:            "code-loader",
			Image:           loaderImage,
			ImagePullPolicy: loaderPullPolicy,
			Command:         []string{"/loader"},
			Env: []corev1.EnvVar{
				{Name: "INDEX_URL", Value: indexURL},
				{Name: "ARTIFACT_NAME", Value: cs.ArtifactName},
				{Name: "ARTIFACT_TAG", Value: cs.Tag},
				{Name: "MOUNT_PATH", Value: mountPath},
			},
			VolumeMounts:    initMounts,
			SecurityContext: containerSC,
		})
	}

	env := make([]corev1.EnvVar, len(tmpl.Spec.Env))
	copy(env, tmpl.Spec.Env)
	if cs := tmpl.Spec.CodeSource; cs != nil {
		mountPath := cs.MountPath
		if mountPath == "" {
			mountPath = "/weave-code"
		}
		env = append(env, codesource.EnvVars(cs.ArtifactName, cs.Tag, version, namespace, mountPath, meta)...)
	}

	var envFrom []corev1.EnvFromSource
	if authSecretName != "" {
		envFrom = []corev1.EnvFromSource{
			{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: authSecretName}}},
		}
	}

	container := corev1.Container{
		Name:            "service",
		Image:           tmpl.Spec.Image,
		Command:         tmpl.Spec.Command,
		Args:            tmpl.Spec.Args,
		Env:             env,
		EnvFrom:         envFrom,
		Resources:       tmpl.Spec.Resources,
		VolumeMounts:    mounts,
		LivenessProbe:   tmpl.Spec.LivenessProbe,
		ReadinessProbe:  tmpl.Spec.ReadinessProbe,
		StartupProbe:    tmpl.Spec.StartupProbe,
		SecurityContext: containerSC,
	}

	for _, p := range tmpl.Spec.Ports {
		container.Ports = append(container.Ports, corev1.ContainerPort{
			Name:          p.Name,
			ContainerPort: effectiveTargetPort(p),
			Protocol:      p.Protocol,
		})
	}

	podLabels := make(map[string]string, len(labels)+len(sec.PodLabels))
	for k, v := range sec.PodLabels {
		podLabels[k] = v
	}
	for k, v := range labels {
		podLabels[k] = v
	}

	var podAnnotations map[string]string
	if len(sec.PodAnnotations) > 0 {
		podAnnotations = make(map[string]string, len(sec.PodAnnotations))
		for k, v := range sec.PodAnnotations {
			podAnnotations[k] = v
		}
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
					Labels:      podLabels,
					Annotations: podAnnotations,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: tmpl.Spec.ServiceAccountName,
					SecurityContext:    podSC,
					InitContainers:     initContainers,
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
// Returns nil when the template has no Ingress spec. hostSuffix is the
// cluster-wide ingress host suffix; each rule's Name is joined with it to
// form the full hostname (see IngressHost).
func BuildIngress(
	tmpl *weavev1alpha1.WeaveServiceTemplate,
	chainName, stepName, namespace, hostSuffix string,
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
			Host: IngressHost(rule.Name, hostSuffix),
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
			hosts = append(hosts, IngressHost(r.Name, hostSuffix))
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

// BuildFromOverride constructs a run-owned Deployment for a deploy-kind step
// that has a WeaveRunStepOverride. The Deployment is named <runName>-<stepName>
// and owned by the WeaveRun (not the WeaveChain). The WeaveServiceTemplate
// provides structural defaults (image, command, volumes, probes, security) and
// AppMetadata overlays runtime fields sourced from the artifact's metadata.yaml
// (runner port, runner args as env vars, resource requests/limits).
// The codeSource on the Deployment is wired to override.ArtifactName / Tag so
// the code-loader init container fetches the correct artifact at pod start.
// The caller must set the OwnerReference.
func BuildFromOverride(
	tmpl *weavev1alpha1.WeaveServiceTemplate,
	override *weavev1alpha1.WeaveRunStepOverride,
	meta *indexclient.AppMetadata,
	runName, stepName, namespace string,
	sec security.Defaults,
	version string,
	defaultIndexURL string,
	defaultLoaderImage string,
	writablePaths []string,
	authSecretName string,
) *appsv1.Deployment {
	name := RunDeploymentName(runName, stepName)
	labels := map[string]string{
		"fusion-platform.io/run":  runName,
		StepLabel:                 stepName,
	}

	podSC := sec.PodSecurityContext
	if tmpl.Spec.PodSecurityContext != nil {
		podSC = tmpl.Spec.PodSecurityContext
	}
	containerSC := sec.ContainerSecurityContext
	if tmpl.Spec.ContainerSecurityContext != nil {
		containerSC = tmpl.Spec.ContainerSecurityContext
	}

	replicas := tmpl.Spec.Replicas
	if replicas == 0 {
		replicas = 1
	}

	revLimit := tmpl.Spec.RevisionHistoryLimit
	volumes, mounts := buildVolumes(tmpl.Spec.Volumes)

	// Build codeSource init container using override artifact/tag.
	indexURL := override.IndexURL
	if indexURL == "" {
		indexURL = defaultIndexURL
	}
	if indexURL == "" {
		indexURL = "http://fusion-index-backend.fusion.svc.cluster.local:8080"
	}
	mountPath := "/weave-code"
	loaderImage := defaultLoaderImage
	if tmpl.Spec.CodeSource != nil {
		if tmpl.Spec.CodeSource.MountPath != "" {
			mountPath = tmpl.Spec.CodeSource.MountPath
		}
		if tmpl.Spec.CodeSource.LoaderImage != "" {
			loaderImage = tmpl.Spec.CodeSource.LoaderImage
		}
	}
	if loaderImage == "" {
		loaderImage = "fusion-code-loader:latest"
	}
	volumes = append(volumes, corev1.Volume{
		Name:         "weave-code",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})
	mounts = append(mounts, corev1.VolumeMount{Name: "weave-code", MountPath: mountPath})
	initMounts := []corev1.VolumeMount{{Name: "weave-code", MountPath: mountPath}}
	for _, p := range writablePaths {
		volName := codesource.WritableVolumeName(p)
		if volName == "" || codesource.HasVolume(volumes, volName) {
			continue
		}
		volumes = append(volumes, corev1.Volume{
			Name:         volName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
		vm := corev1.VolumeMount{Name: volName, MountPath: p}
		mounts = append(mounts, vm)
		initMounts = append(initMounts, vm)
	}
	initContainers := []corev1.Container{{
		Name:            "code-loader",
		Image:           loaderImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"/loader"},
		Env: []corev1.EnvVar{
			{Name: "INDEX_URL", Value: indexURL},
			{Name: "ARTIFACT_NAME", Value: override.ArtifactName},
			{Name: "ARTIFACT_TAG", Value: override.Tag},
			{Name: "MOUNT_PATH", Value: mountPath},
		},
		VolumeMounts:    initMounts,
		SecurityContext: containerSC,
	}}

	// Merge env: template env first, then standard WEAVE_* vars + runner.args from metadata.
	env := make([]corev1.EnvVar, len(tmpl.Spec.Env))
	copy(env, tmpl.Spec.Env)
	env = append(env, codesource.EnvVars(override.ArtifactName, override.Tag, version, namespace, mountPath, meta)...)

	// Resources: metadata wins when non-empty, otherwise fall back to template.
	resources := tmpl.Spec.Resources
	if meta != nil && (len(meta.Resources.Requests) > 0 || len(meta.Resources.Limits) > 0) {
		resources = meta.Resources
	}

	// Ports: metadata runner.port wins when set, otherwise use template ports.
	ports := tmpl.Spec.Ports
	if meta != nil && meta.Runner.Port > 0 {
		ports = []weavev1alpha1.WeaveServicePort{{
			Name:       "http",
			Port:       meta.Runner.Port,
			TargetPort: meta.Runner.Port,
			Protocol:   corev1.ProtocolTCP,
		}}
	}

	var containerPorts []corev1.ContainerPort
	for _, p := range ports {
		containerPorts = append(containerPorts, corev1.ContainerPort{
			Name:          p.Name,
			ContainerPort: effectiveTargetPort(p),
			Protocol:      p.Protocol,
		})
	}

	var envFrom []corev1.EnvFromSource
	if authSecretName != "" {
		envFrom = []corev1.EnvFromSource{
			{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: authSecretName}}},
		}
	}

	container := corev1.Container{
		Name:            "service",
		Image:           tmpl.Spec.Image,
		Command:         tmpl.Spec.Command,
		Args:            tmpl.Spec.Args,
		Env:             env,
		EnvFrom:         envFrom,
		Resources:       resources,
		VolumeMounts:    mounts,
		Ports:           containerPorts,
		LivenessProbe:   tmpl.Spec.LivenessProbe,
		ReadinessProbe:  tmpl.Spec.ReadinessProbe,
		StartupProbe:    tmpl.Spec.StartupProbe,
		SecurityContext: containerSC,
	}

	podLabels := make(map[string]string, len(labels)+len(sec.PodLabels))
	for k, v := range sec.PodLabels {
		podLabels[k] = v
	}
	for k, v := range labels {
		podLabels[k] = v
	}

	var podAnnotations map[string]string
	if len(sec.PodAnnotations) > 0 {
		podAnnotations = make(map[string]string, len(sec.PodAnnotations))
		for k, v := range sec.PodAnnotations {
			podAnnotations[k] = v
		}
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
					Labels:      podLabels,
					Annotations: podAnnotations,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: tmpl.Spec.ServiceAccountName,
					SecurityContext:    podSC,
					InitContainers:     initContainers,
					Containers:         []corev1.Container{container},
					Volumes:            volumes,
				},
			},
		},
	}
}

// BuildServiceFromOverride constructs a run-owned Service for a step override.
func BuildServiceFromOverride(
	tmpl *weavev1alpha1.WeaveServiceTemplate,
	meta *indexclient.AppMetadata,
	runName, stepName, namespace string,
) *corev1.Service {
	name := RunServiceName(runName, stepName)
	labels := map[string]string{
		"fusion-platform.io/run": runName,
		StepLabel:                stepName,
	}

	ports := tmpl.Spec.Ports
	if meta != nil && meta.Runner.Port > 0 {
		ports = []weavev1alpha1.WeaveServicePort{{
			Name:       "http",
			Port:       meta.Runner.Port,
			TargetPort: meta.Runner.Port,
			Protocol:   corev1.ProtocolTCP,
		}}
	}

	var svcPorts []corev1.ServicePort
	for _, p := range ports {
		svcPorts = append(svcPorts, corev1.ServicePort{
			Name:       p.Name,
			Port:       p.Port,
			TargetPort: intstr.FromInt32(effectiveTargetPort(p)),
			Protocol:   p.Protocol,
		})
	}

	svcType := tmpl.Spec.ServiceType
	if svcType == "" {
		svcType = corev1.ServiceTypeClusterIP
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
			Ports:    svcPorts,
		},
	}
}

// BuildIngressFromOverride constructs a run-owned Ingress for a step override.
// override.IngressName overrides the name from the template's ingress rules.
// meta.Ingress.PathPrefix, when set, replaces the path in the first rule.
// hostSuffix is the cluster-wide ingress host suffix; the resolved name is
// joined with it to form the full hostname (see IngressHost).
// Returns nil when the template has no Ingress spec and override.IngressName is empty.
func BuildIngressFromOverride(
	tmpl *weavev1alpha1.WeaveServiceTemplate,
	meta *indexclient.AppMetadata,
	override *weavev1alpha1.WeaveRunStepOverride,
	runName, stepName, namespace, hostSuffix string,
) *networkingv1.Ingress {
	if tmpl.Spec.Ingress == nil && override.IngressName == "" {
		return nil
	}

	name := RunIngressName(runName, stepName)
	svcName := RunServiceName(runName, stepName)
	labels := map[string]string{
		"fusion-platform.io/run": runName,
		StepLabel:                stepName,
	}

	// Determine port reference: prefer metadata port, fall back to template.
	var servicePort networkingv1.ServiceBackendPort
	if meta != nil && meta.Runner.Port > 0 {
		servicePort = networkingv1.ServiceBackendPort{Number: meta.Runner.Port}
	} else if tmpl.Spec.Ingress != nil && len(tmpl.Spec.Ingress.Rules) > 0 {
		servicePort = resolveServicePort(tmpl.Spec.Ingress.Rules[0].ServicePort, tmpl.Spec.Ports)
	} else if len(tmpl.Spec.Ports) > 0 {
		servicePort = networkingv1.ServiceBackendPort{Number: tmpl.Spec.Ports[0].Port}
	}

	path := "/"
	if meta != nil && meta.Ingress.PathPrefix != "" {
		path = "/" + meta.Ingress.PathPrefix
	} else if tmpl.Spec.Ingress != nil && len(tmpl.Spec.Ingress.Rules) > 0 {
		path = tmpl.Spec.Ingress.Rules[0].Path
	}

	pathType := networkingv1.PathTypePrefix
	ruleName := override.IngressName
	if ruleName == "" && tmpl.Spec.Ingress != nil && len(tmpl.Spec.Ingress.Rules) > 0 {
		ruleName = tmpl.Spec.Ingress.Rules[0].Name
	}
	host := IngressHost(ruleName, hostSuffix)

	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{
				Host: host,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     path,
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: svcName,
									Port: servicePort,
								},
							},
						}},
					},
				},
			}},
		},
	}

	if tmpl.Spec.Ingress != nil {
		if tmpl.Spec.Ingress.IngressClassName != nil {
			ing.Spec.IngressClassName = tmpl.Spec.Ingress.IngressClassName
		}
		if tmpl.Spec.Ingress.TLSSecretName != "" {
			ing.Spec.TLS = []networkingv1.IngressTLS{{
				Hosts:      []string{host},
				SecretName: tmpl.Spec.Ingress.TLSSecretName,
			}}
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
