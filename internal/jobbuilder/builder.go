// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package jobbuilder translates WeaveJobTemplate + WeaveChainStep + WeaveRun
// into a batch/v1 Job ready to be created in Kubernetes.
package jobbuilder

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/security"
)

// inputVolumeName is the volume name used when mounting upstream step outputs.
const inputVolumeName = "weave-input"

// InputFilePath is the path inside the container where merged upstream JSON is available.
const InputFilePath = "/weave-input/input.json"

// sharedVolumeName is the pod volume name for the per-run shared PVC.
const sharedVolumeName = "weave-shared"

// SharedMountPath is the path inside the container where the shared PVC is mounted.
const SharedMountPath = "/weave-shared"

// SharedPVCName returns the deterministic name for the per-run shared PVC.
func SharedPVCName(runName string) string {
	return runName + "-shared"
}

// InputConfigMapKey returns the key within the run output ConfigMap that holds
// the merged input JSON for the named consuming step.
func InputConfigMapKey(stepName string) string {
	return "input-" + stepName
}

// OutputConfigMapKey returns the key within the run output ConfigMap that holds
// the captured JSON stdout for the named producing step.
func OutputConfigMapKey(stepName string) string {
	return "step-" + stepName
}

// OutputsConfigMapName returns the name of the per-run ConfigMap that stores
// step output data.
func OutputsConfigMapName(runName string) string {
	return runName + "-outputs"
}

// JobName returns the deterministic name for a step's batch Job.
// Format: <runName>-<stepName>-<retryCount>
func JobName(runName, stepName string, retryCount int32) string {
	suffix := fmt.Sprintf("%s-%s-%d", runName, stepName, retryCount)
	// Kubernetes names must be <=253 chars and DNS subdomain safe.
	if len(suffix) > 253 {
		suffix = suffix[:253]
	}
	return suffix
}

// Build constructs a batch/v1 Job for the given step.
// The job is owned by the WeaveRun (ownerRef must be set by the caller).
// inputConfigMap is the name of the run's output ConfigMap; pass a non-empty
// value only when the step has ConsumesOutputFrom entries and the merged input
// JSON key has already been written to the ConfigMap.
// sec carries operator-wide security defaults applied to the pod and container.
func Build(
	template *weavev1alpha1.WeaveJobTemplate,
	step *weavev1alpha1.WeaveChainStep,
	run *weavev1alpha1.WeaveRun,
	retryCount int32,
	inputConfigMap string,
	sharedPVCName string,
	sec security.Defaults,
	authSecretName string,
) *batchv1.Job {
	name := JobName(run.Name, step.Name, retryCount)
	ns := run.Namespace

	// Merge environment variables: template base → step overrides → run params.
	env := mergeEnv(template.Spec.Env, step.EnvOverrides, run.Spec.ParameterOverrides)

	// Build volumes and mounts from WeaveVolumeMount declarations.
	volumes, mounts := buildVolumes(template.Spec.Volumes)

	// Mount merged upstream JSON when this step consumes output from prior steps.
	if inputConfigMap != "" {
		key := InputConfigMapKey(step.Name)
		volumes = append(volumes, corev1.Volume{
			Name: inputVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: inputConfigMap},
					Items: []corev1.KeyToPath{
						{Key: key, Path: "input.json"},
					},
				},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      inputVolumeName,
			MountPath: "/weave-input",
			ReadOnly:  true,
		})
	}

	// Mount the per-run shared PVC when the chain has SharedStorage configured.
	if sharedPVCName != "" {
		volumes = append(volumes, corev1.Volume{
			Name: sharedVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: sharedPVCName,
				},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      sharedVolumeName,
			MountPath: SharedMountPath,
		})
	}

	// Resolve effective security contexts: template fields override global defaults.
	podSC := sec.PodSecurityContext
	if template.Spec.PodSecurityContext != nil {
		podSC = template.Spec.PodSecurityContext
	}
	containerSC := sec.ContainerSecurityContext
	if template.Spec.ContainerSecurityContext != nil {
		containerSC = template.Spec.ContainerSecurityContext
	}

	var envFrom []corev1.EnvFromSource
	if authSecretName != "" {
		envFrom = []corev1.EnvFromSource{
			{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: authSecretName}}},
		}
	}

	parallelism := template.Spec.Parallelism
	completions := template.Spec.Completions

	// backoffLimit is always 0 — the operator manages retries itself.
	backoffLimit := int32(0)

	podLabels := make(map[string]string, 3+len(sec.PodLabels))
	for k, v := range sec.PodLabels {
		podLabels[k] = v
	}
	podLabels["fusion-platform.io/run"] = run.Name
	podLabels["fusion-platform.io/step"] = step.Name
	podLabels["fusion-platform.io/chain"] = run.Spec.ChainRef.Name

	var podAnnotations map[string]string
	if len(sec.PodAnnotations) > 0 {
		podAnnotations = make(map[string]string, len(sec.PodAnnotations))
		for k, v := range sec.PodAnnotations {
			podAnnotations[k] = v
		}
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"fusion-platform.io/run":   run.Name,
				"fusion-platform.io/step":  step.Name,
				"fusion-platform.io/chain": run.Spec.ChainRef.Name,
			},
		},
		Spec: batchv1.JobSpec{
			Parallelism:           &parallelism,
			Completions:           &completions,
			ActiveDeadlineSeconds: template.Spec.ActiveDeadlineSeconds,
			BackoffLimit:          &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      podLabels,
					Annotations: podAnnotations,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: template.Spec.ServiceAccountName,
					SecurityContext:    podSC,
					Containers: []corev1.Container{
						{
							Name:            "job",
							Image:           template.Spec.Image,
							Command:         template.Spec.Command,
							Args:            template.Spec.Args,
							Env:             env,
							EnvFrom:         envFrom,
							Resources:       template.Spec.Resources,
							VolumeMounts:    mounts,
							SecurityContext: containerSC,
						},
					},
					Volumes: volumes,
				},
			},
		},
	}

	return job
}

// mergeEnv merges environment variable slices in order; later entries win.
func mergeEnv(slices ...[]corev1.EnvVar) []corev1.EnvVar {
	seen := map[string]int{}
	result := []corev1.EnvVar{}

	for _, envs := range slices {
		for _, e := range envs {
			if idx, ok := seen[e.Name]; ok {
				result[idx] = e
			} else {
				seen[e.Name] = len(result)
				result = append(result, e)
			}
		}
	}
	return result
}

// buildVolumes converts WeaveVolumeMount declarations into pod Volumes and
// container VolumeMounts.
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
