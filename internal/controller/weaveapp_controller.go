// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/deploybuilder"
	"fusion-platform.io/fusion-weave/internal/imagepolicy"
	"fusion-platform.io/fusion-weave/internal/indexclient"
	"fusion-platform.io/fusion-weave/internal/jobbuilder"
	"fusion-platform.io/fusion-weave/internal/security"
	"fusion-platform.io/fusion-weave/internal/trigger"
)

const (
	// FireAnnotation on a WeaveApp fires the named entrypoint once on demand.
	// The reconciler consumes (removes) it.
	FireAnnotation = "fusion-platform.io/fire"

	// AppLabel / EntrypointLabel mark every Job a WeaveApp creates.
	AppLabel        = "fusion-platform.io/app"
	EntrypointLabel = "fusion-platform.io/entrypoint"

	// appStepName is the step name of a type=service app's Deployment/Service/Ingress
	// (resource name: <app>-app).
	appStepName = "app"

	maxAppExecutions       = 20
	defaultExecutionTTL    = int32(24 * 60 * 60)
	appCronPendingWakeSize = 64
)

// appCronParser mirrors cron.WithSeconds() used by trigger.CronScheduler, so a
// schedule is validated with the same grammar it is later registered with.
var appCronParser = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// WeaveAppReconciler reconciles WeaveApp: one service or job whose Kubernetes
// objects are all owned by the WeaveApp itself (no template/chain/trigger/run).
// It reuses jobbuilder and deploybuilder through in-memory template objects that
// are never persisted.
type WeaveAppReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	KubeClient kubernetes.Interface

	SecurityDefaults       security.Defaults
	FusionIndexURL         string
	LoaderImage            string
	WritablePaths          []string
	Ingress                deploybuilder.IngressConfig
	CodeSourcePollInterval time.Duration
	// RunnerWritablePaths is RUNNER_WRITABLE_PATHS: extra emptyDir paths keyed by
	// the artifact's metadata.yaml runner.type.
	RunnerWritablePaths map[string][]string
	// AllowedImagePrefixes is ALLOWED_IMAGE_PREFIXES; empty = images unrestricted.
	AllowedImagePrefixes []string

	ExternalAuthServiceAccounts []string
	ExternalAuthOIDCSecrets     []string
	ExternalAuthDefaultTTL      time.Duration
	ExternalAuthSAAudience      string

	cron    *trigger.CronScheduler
	panicCh chan trigger.TriggerPanic
	wake    chan event.GenericEvent

	mu         sync.Mutex
	pending    map[types.NamespacedName]map[string]struct{} // cron fires awaiting a reconcile
	registered map[types.NamespacedName]map[string]string   // entrypoint -> schedule currently in cron
}

// NewWeaveAppReconciler wires the cron scheduler and wake-up plumbing. cronScheduler
// must have been created with the returned reconciler's panic channel — use
// NewAppCronScheduler for that.
func NewWeaveAppReconciler(c client.Client, scheme *runtime.Scheme, cronScheduler *trigger.CronScheduler, panicCh chan trigger.TriggerPanic) *WeaveAppReconciler {
	return &WeaveAppReconciler{
		Client:     c,
		Scheme:     scheme,
		cron:       cronScheduler,
		panicCh:    panicCh,
		wake:       make(chan event.GenericEvent, appCronPendingWakeSize),
		pending:    map[types.NamespacedName]map[string]struct{}{},
		registered: map[types.NamespacedName]map[string]string{},
	}
}

var weaveAppGVK = weavev1alpha1.GroupVersion.WithKind("WeaveApp")

// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=weaveapps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=weave.fusion-platform.io,resources=weaveapps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",resources=serviceaccounts/token,verbs=create

func (r *WeaveAppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var app weavev1alpha1.WeaveApp
	if err := r.Get(ctx, req.NamespacedName, &app); err != nil {
		if errors.IsNotFound(err) {
			// Owner references garbage-collect everything the app created.
			r.unregisterSchedules(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !app.DeletionTimestamp.IsZero() {
		r.unregisterSchedules(req.NamespacedName)
		return ctrl.Result{}, nil
	}
	app.TypeMeta = metav1.TypeMeta{APIVersion: weavev1alpha1.GroupVersion.String(), Kind: weaveAppGVK.Kind}

	orig := app.DeepCopy()
	app.Status.ObservedGeneration = app.Generation

	var (
		result ctrl.Result
		err    error
	)
	if msg := r.validate(&app); msg != "" {
		r.unregisterSchedules(req.NamespacedName)
		app.Status.Phase = weavev1alpha1.WeaveAppPhaseInvalid
		app.Status.ValidationMessage = msg
	} else {
		app.Status.ValidationMessage = ""
		switch app.Spec.Type {
		case weavev1alpha1.WeaveAppTypeService:
			result, err = r.reconcileService(ctx, &app)
		case weavev1alpha1.WeaveAppTypeJob:
			result, err = r.reconcileJob(ctx, &app)
		}
		if err != nil {
			logger.Error(err, "reconcile weaveapp", "app", req.NamespacedName)
		}
	}

	if !equality.Semantic.DeepEqual(orig.Status, app.Status) {
		if statusErr := r.Status().Patch(ctx, &app, client.MergeFrom(orig)); statusErr != nil {
			if err == nil {
				err = statusErr
			}
		}
	}
	return result, err
}

// validate returns a human-readable reason when the spec is unusable, "" when valid.
func (r *WeaveAppReconciler) validate(app *weavev1alpha1.WeaveApp) string {
	spec := &app.Spec
	if msg := validateAppImages(spec, r.AllowedImagePrefixes); msg != "" {
		return msg
	}
	if msg := validateAppWritablePaths(spec); msg != "" {
		return msg
	}
	switch spec.Type {
	case weavev1alpha1.WeaveAppTypeService:
		if len(spec.Entrypoints) > 0 {
			return "entrypoints are only supported for type=job"
		}
		if spec.ExternalAuthRef != nil {
			return "externalAuthRef is only supported for type=job"
		}
		svc := spec.Service
		if svc == nil {
			svc = &weavev1alpha1.WeaveAppService{}
		}
		if len(svc.Ports) == 0 && spec.CodeSource == nil {
			return "service.ports is required unless codeSource provides runner.port in metadata.yaml"
		}
		if msg := validateAppIngress(svc, r.Ingress.HostSuffix); msg != "" {
			return msg
		}
	case weavev1alpha1.WeaveAppTypeJob:
		if spec.Service != nil {
			return "service is only supported for type=service"
		}
		if len(spec.Entrypoints) == 0 {
			return "type=job requires at least one entrypoint"
		}
		seen := map[string]bool{}
		for _, ep := range spec.Entrypoints {
			if seen[ep.Name] {
				return fmt.Sprintf("duplicate entrypoint name %q", ep.Name)
			}
			seen[ep.Name] = true
			if ep.Schedule != "" {
				if _, err := appCronParser.Parse(ep.Schedule); err != nil {
					return fmt.Sprintf("entrypoint %q: invalid schedule: %v", ep.Name, err)
				}
			}
		}
		if spec.ExternalAuthRef != nil {
			if err := checkExternalAuthAllowed(spec.ExternalAuthRef, r.ExternalAuthServiceAccounts, r.ExternalAuthOIDCSecrets); err != nil {
				return err.Error()
			}
		}
	default:
		return fmt.Sprintf("type %q must be service or job", spec.Type)
	}
	return ""
}

// ---- type=service ----

// validateAppImages applies the image policy to the workload image and to a
// custom code-loader image (the operator default loader is trusted).
func validateAppImages(spec *weavev1alpha1.WeaveAppSpec, allowed []string) string {
	if err := imagepolicy.ValidateApp(spec.Image, allowed); err != nil {
		return "spec.image: " + err.Error()
	}
	if cs := spec.CodeSource; cs != nil && cs.LoaderImage != "" {
		if err := imagepolicy.ValidateApp(cs.LoaderImage, allowed); err != nil {
			return "spec.codeSource.loaderImage: " + err.Error()
		}
	}
	return ""
}

func validateAppIngress(svc *weavev1alpha1.WeaveAppService, hostSuffix string) string {
	if svc.IngressName == "" && svc.Ingress == nil {
		if len(svc.IngressAnnotations) > 0 {
			return "service.ingressAnnotations requires service.ingressName or service.ingress"
		}
		return ""
	}
	if svc.IngressName != "" && svc.Ingress != nil {
		return "service.ingressName and service.ingress are mutually exclusive"
	}
	if hostSuffix == "" {
		return "service has an ingress but the operator ingress host suffix is not configured (ingress.hostSuffix)"
	}
	if svc.Ingress == nil {
		return ""
	}
	if len(svc.Ingress.Rules) == 0 {
		return "service.ingress.rules must contain at least one rule"
	}
	portNames := map[string]bool{}
	for _, p := range svc.Ports {
		portNames[p.Name] = true
	}
	for _, rule := range svc.Ingress.Rules {
		if rule.ServicePort == "" || len(svc.Ports) == 0 {
			continue
		}
		if _, err := strconv.Atoi(rule.ServicePort); err != nil && !portNames[rule.ServicePort] {
			return fmt.Sprintf("ingress rule %q references servicePort %q which is not declared in service.ports", rule.Name, rule.ServicePort)
		}
	}
	return ""
}

// overlayAppIngressAnnotations layers the runner-type annotations
// (ingress.runnerAnnotations[metadata runner.type]) and then the app's own
// service.ingressAnnotations onto the instance-wide ones; later layers win.
func overlayAppIngressAnnotations(ing *networkingv1.Ingress, cfg deploybuilder.IngressConfig, meta *indexclient.AppMetadata, svc *weavev1alpha1.WeaveAppService) {
	if meta != nil && meta.Runner.Type != "" {
		ing.Annotations = mergeStringMaps(ing.Annotations, cfg.RunnerAnnotations[meta.Runner.Type])
	}
	ing.Annotations = mergeStringMaps(ing.Annotations, svc.IngressAnnotations)
}

// ingressSpecFor expands the ingressName shorthand or the full ingress block
// into the WeaveIngressSpec deploybuilder.BuildIngress consumes. Nil = no ingress.
// Rules without a servicePort use the artifact's runner.port, else the first
// declared port, so the shorthand works for codeSource apps with no ports.
func ingressSpecFor(svc *weavev1alpha1.WeaveAppService, meta *indexclient.AppMetadata) *weavev1alpha1.WeaveIngressSpec {
	if svc == nil || (svc.IngressName == "" && svc.Ingress == nil) {
		return nil
	}
	defaultPort := ""
	switch {
	case meta != nil && meta.Runner.Port > 0:
		defaultPort = strconv.Itoa(int(meta.Runner.Port))
	case len(svc.Ports) > 0:
		defaultPort = svc.Ports[0].Name
	}

	if svc.Ingress == nil {
		return &weavev1alpha1.WeaveIngressSpec{Rules: []weavev1alpha1.WeaveIngressRule{{
			Name: svc.IngressName, Path: "/", PathType: "Prefix", ServicePort: defaultPort,
		}}}
	}
	spec := &weavev1alpha1.WeaveIngressSpec{
		IngressClassName: svc.Ingress.IngressClassName,
		TLSSecretName:    svc.Ingress.TLSSecretName,
	}
	for _, rule := range svc.Ingress.Rules {
		sp := rule.ServicePort
		if sp == "" {
			sp = defaultPort
		}
		spec.Rules = append(spec.Rules, weavev1alpha1.WeaveIngressRule{
			Name: rule.Name, Path: rule.Path, PathType: rule.PathType, ServicePort: sp,
		})
	}
	return spec
}

// serviceTemplateFor builds the in-memory WeaveServiceTemplate deploybuilder needs.
func serviceTemplateFor(app *weavev1alpha1.WeaveApp) *weavev1alpha1.WeaveServiceTemplate {
	svc := app.Spec.Service
	if svc == nil {
		svc = &weavev1alpha1.WeaveAppService{}
	}
	return &weavev1alpha1.WeaveServiceTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: app.Name, Namespace: app.Namespace},
		Spec: weavev1alpha1.WeaveServiceTemplateSpec{
			Image:                    app.Spec.Image,
			Command:                  app.Spec.Command,
			Args:                     app.Spec.Args,
			Env:                      app.Spec.Env,
			Resources:                app.Spec.Resources,
			Volumes:                  app.Spec.Volumes,
			ServiceAccountName:       app.Spec.ServiceAccountName,
			PodSecurityContext:       app.Spec.PodSecurityContext,
			ContainerSecurityContext: app.Spec.ContainerSecurityContext,
			Replicas:                 svc.Replicas,
			Ports:                    svc.Ports,
			LivenessProbe:            svc.LivenessProbe,
			ReadinessProbe:           svc.ReadinessProbe,
			StartupProbe:             svc.StartupProbe,
			ServiceType:              svc.ServiceType,
			CodeSource:               app.Spec.CodeSource,
		},
	}
}

func (r *WeaveAppReconciler) resolveIndexURL(explicit string) string {
	switch {
	case explicit != "":
		return explicit
	case r.FusionIndexURL != "":
		return r.FusionIndexURL
	}
	return "http://fusion-index-backend.fusion.svc.cluster.local:8080"
}

func (r *WeaveAppReconciler) reconcileService(ctx context.Context, app *weavev1alpha1.WeaveApp) (ctrl.Result, error) {
	r.unregisterSchedules(client.ObjectKeyFromObject(app))
	app.Status.Executions = nil

	tmpl := serviceTemplateFor(app)
	override := &weavev1alpha1.WeaveRunStepOverride{StepName: appStepName}

	var meta *indexclient.AppMetadata
	var version string
	if cs := app.Spec.CodeSource; cs != nil {
		override.ArtifactName, override.Tag, override.IndexURL = cs.ArtifactName, cs.Tag, cs.IndexURL
		var err error
		meta, version, err = indexclient.FetchAppMetadataAndVersion(ctx, r.resolveIndexURL(cs.IndexURL), cs.ArtifactName, cs.Tag)
		if err != nil {
			app.Status.Phase = weavev1alpha1.WeaveAppPhasePending
			return ctrl.Result{}, fmt.Errorf("fetch app metadata for %s@%s: %w", cs.ArtifactName, cs.Tag, err)
		}
	}

	ownerRef := metav1.NewControllerRef(app, weaveAppGVK)
	deployName := deploybuilder.RunDeploymentName(app.Name, appStepName)

	desired := deploybuilder.BuildFromOverride(tmpl, override, meta, app.Name, appStepName, app.Namespace,
		r.SecurityDefaults, version, r.FusionIndexURL, r.LoaderImage, r.WritablePaths, "", false)
	applyWritablePaths(&desired.Spec.Template.Spec, appWritablePaths(r.RunnerWritablePaths, meta, &app.Spec))
	desired.OwnerReferences = []metav1.OwnerReference{*ownerRef}

	var existing appsv1.Deployment
	err := r.Get(ctx, types.NamespacedName{Namespace: app.Namespace, Name: deployName}, &existing)
	switch {
	case errors.IsNotFound(err):
		if createErr := r.Create(ctx, desired); createErr != nil && !errors.IsAlreadyExists(createErr) {
			return ctrl.Result{}, fmt.Errorf("create deployment %q: %w", deployName, createErr)
		}
		existing = *desired
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("get deployment %q: %w", deployName, err)
	default:
		patch := client.MergeFrom(existing.DeepCopy())
		existing.Spec.Template = desired.Spec.Template
		existing.Spec.Replicas = desired.Spec.Replicas
		if err := r.Patch(ctx, &existing, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch deployment %q: %w", deployName, err)
		}
	}
	app.Status.DeploymentName = deployName

	svc := deploybuilder.BuildServiceFromOverride(tmpl, meta, app.Name, appStepName, app.Namespace)
	svc.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	if err := upsertService(ctx, r.Client, svc); err != nil {
		return ctrl.Result{}, fmt.Errorf("upsert service %q: %w", svc.Name, err)
	}

	// The chain-owned BuildIngress is used (not BuildIngressFromOverride): it
	// supports several rules, pathType, ingressClassName and tlsSecretName. Its
	// Service/Ingress names equal the Run* ones for (app.Name, appStepName).
	tmpl.Spec.Ingress = ingressSpecFor(app.Spec.Service, meta)
	ing := deploybuilder.BuildIngress(tmpl, app.Name, appStepName, app.Namespace, r.Ingress)
	if ing != nil {
		// The runner serves under the artifact's ingress.path, so it must be routed.
		if meta != nil && meta.Ingress.Path != "" && len(ing.Spec.Rules) > 0 {
			ing.Spec.Rules[0].HTTP.Paths[0].Path = indexclient.NormalizeIngressPath(meta.Ingress.Path)
		}
		overlayAppIngressAnnotations(ing, r.Ingress, meta, app.Spec.Service)
		ing.OwnerReferences = []metav1.OwnerReference{*ownerRef}
		if err := upsertIngress(ctx, r.Client, ing); err != nil {
			return ctrl.Result{}, fmt.Errorf("upsert ingress %q: %w", ing.Name, err)
		}
	} else {
		// The ingress was removed from the spec — drop the Ingress we created earlier.
		stale := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{
			Name: deploybuilder.IngressName(app.Name, appStepName), Namespace: app.Namespace}}
		if err := r.Delete(ctx, stale); err != nil && !errors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete stale ingress: %w", err)
		}
	}

	if isDeploymentAvailable(&existing) {
		app.Status.Phase = weavev1alpha1.WeaveAppPhaseReady
	} else {
		app.Status.Phase = weavev1alpha1.WeaveAppPhasePending
	}

	if app.Spec.CodeSource != nil {
		// A moved tag changes WEAVE_VERSION in the rebuilt pod template, which is
		// itself the rolling restart; re-resolving on an interval is all it takes.
		app.Status.CodeSourceDeployedVersion = version
		return ctrl.Result{RequeueAfter: r.pollInterval()}, nil
	}
	return ctrl.Result{}, nil
}

func (r *WeaveAppReconciler) pollInterval() time.Duration {
	if r.CodeSourcePollInterval > 0 {
		return r.CodeSourcePollInterval
	}
	return 60 * time.Second
}

// ---- type=job ----

// jobTemplateFor builds the in-memory WeaveJobTemplate for one entrypoint.
func jobTemplateFor(app *weavev1alpha1.WeaveApp, ep *weavev1alpha1.WeaveAppEntrypoint) *weavev1alpha1.WeaveJobTemplate {
	command, args := app.Spec.Command, app.Spec.Args
	if len(ep.Command) > 0 {
		command = ep.Command
	}
	if len(ep.Args) > 0 {
		args = ep.Args
	}
	return &weavev1alpha1.WeaveJobTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: app.Name, Namespace: app.Namespace},
		Spec: weavev1alpha1.WeaveJobTemplateSpec{
			Image:                    app.Spec.Image,
			Command:                  command,
			Args:                     args,
			Env:                      app.Spec.Env,
			Resources:                app.Spec.Resources,
			Volumes:                  app.Spec.Volumes,
			Parallelism:              1,
			Completions:              1,
			ActiveDeadlineSeconds:    app.Spec.ActiveDeadlineSeconds,
			ServiceAccountName:       app.Spec.ServiceAccountName,
			PodSecurityContext:       app.Spec.PodSecurityContext,
			ContainerSecurityContext: app.Spec.ContainerSecurityContext,
			CodeSource:               app.Spec.CodeSource,
		},
	}
}

func (r *WeaveAppReconciler) reconcileJob(ctx context.Context, app *weavev1alpha1.WeaveApp) (ctrl.Result, error) {
	nn := client.ObjectKeyFromObject(app)

	// Persisted executions first, so the overlap check below sees current state.
	if err := r.syncExecutions(ctx, app); err != nil {
		return ctrl.Result{}, err
	}

	r.syncSchedules(app)

	// Collect fires: cron ticks plus the on-demand annotation.
	fires := r.takePendingFires(nn)
	if ep := app.Annotations[FireAnnotation]; ep != "" {
		fires[ep] = struct{}{}
		// Patch a copy: Patch overwrites its argument with the server response,
		// which would discard the in-memory status computed so far.
		stripped := app.DeepCopy()
		delete(stripped.Annotations, FireAnnotation)
		if err := r.Patch(ctx, stripped, client.MergeFrom(app.DeepCopy())); err != nil {
			return ctrl.Result{}, fmt.Errorf("consume fire annotation: %w", err)
		}
		delete(app.Annotations, FireAnnotation)
	}

	logger := log.FromContext(ctx)
	for _, ep := range app.Spec.Entrypoints {
		if _, ok := fires[ep.Name]; !ok {
			continue
		}
		delete(fires, ep.Name)
		if hasRunningExecution(app, ep.Name) {
			logger.Info("skipping fire: previous execution still running", "app", app.Name, "entrypoint", ep.Name)
			continue
		}
		exec := r.launchExecution(ctx, app, &ep)
		app.Status.Executions = append([]weavev1alpha1.WeaveAppExecution{exec}, app.Status.Executions...)
	}
	for unknown := range fires {
		logger.Info("ignoring fire for unknown entrypoint", "app", app.Name, "entrypoint", unknown)
	}
	if len(app.Status.Executions) > maxAppExecutions {
		app.Status.Executions = app.Status.Executions[:maxAppExecutions]
	}

	if hasAnyRunningExecution(app) {
		app.Status.Phase = weavev1alpha1.WeaveAppPhaseRunning
	} else {
		app.Status.Phase = weavev1alpha1.WeaveAppPhaseReady
	}
	return ctrl.Result{}, nil
}

func hasRunningExecution(app *weavev1alpha1.WeaveApp, entrypoint string) bool {
	for _, e := range app.Status.Executions {
		if e.Entrypoint == entrypoint && e.Phase == weavev1alpha1.WeaveAppExecutionRunning {
			return true
		}
	}
	return false
}

func hasAnyRunningExecution(app *weavev1alpha1.WeaveApp) bool {
	for _, e := range app.Status.Executions {
		if e.Phase == weavev1alpha1.WeaveAppExecutionRunning {
			return true
		}
	}
	return false
}

// launchExecution creates the Job (and the ephemeral external-auth Secret) for one
// fire of ep. It never returns an error: a failed launch is recorded as a Failed
// execution so the user sees it in status.
func (r *WeaveAppReconciler) launchExecution(ctx context.Context, app *weavev1alpha1.WeaveApp, ep *weavev1alpha1.WeaveAppEntrypoint) weavev1alpha1.WeaveAppExecution {
	now := metav1.Now()
	runName := fmt.Sprintf("%s-%s", app.Name, now.UTC().Format("060102-150405"))
	jobName := jobbuilder.JobName(runName, ep.Name, 0)
	exec := weavev1alpha1.WeaveAppExecution{
		Entrypoint: ep.Name, JobName: jobName, StartTime: &now,
		Phase: weavev1alpha1.WeaveAppExecutionRunning,
	}
	fail := func(format string, a ...any) weavev1alpha1.WeaveAppExecution {
		exec.Phase = weavev1alpha1.WeaveAppExecutionFailed
		exec.CompletionTime = &now
		exec.Message = fmt.Sprintf(format, a...)
		log.FromContext(ctx).Info("execution launch failed", "app", app.Name, "entrypoint", ep.Name, "reason", exec.Message)
		return exec
	}

	ownerRef := metav1.NewControllerRef(app, weaveAppGVK)
	tmpl := jobTemplateFor(app, ep)

	var csMeta *indexclient.AppMetadata
	var csVersion string
	if cs := app.Spec.CodeSource; cs != nil {
		// Best-effort, like the chain path: the code-loader resolves the tag itself.
		var err error
		csMeta, csVersion, err = indexclient.FetchAppMetadataAndVersion(ctx, r.resolveIndexURL(cs.IndexURL), cs.ArtifactName, cs.Tag)
		if err != nil {
			log.FromContext(ctx).Error(err, "fetch codeSource metadata (best-effort)", "artifact", cs.ArtifactName, "tag", cs.Tag)
			csMeta, csVersion = nil, ""
		}
	}

	extRef := app.Spec.ExternalAuthRef
	extSecretName := ""
	if extRef != nil {
		extSecretName = jobbuilder.ExternalAuthSecretName(jobName)
		ttl := r.ExternalAuthDefaultTTL
		if tmpl.Spec.ActiveDeadlineSeconds != nil {
			ttl = time.Duration(*tmpl.Spec.ActiveDeadlineSeconds)*time.Second + 60*time.Second
		}
		token, err := mintExternalAuthToken(ctx, r, r.KubeClient, r.ExternalAuthSAAudience, app.Namespace, extRef, ttl)
		if err != nil {
			return fail("failed to mint external auth token: %v", err)
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: extSecretName, Namespace: app.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef}},
			StringData: map[string]string{"token": token},
		}
		if err := r.Create(ctx, secret); err != nil && !errors.IsAlreadyExists(err) {
			return fail("failed to create external auth secret: %v", err)
		}
	}

	unsafeEnv := app.Spec.UnsafeEnvironmentInjector == nil || *app.Spec.UnsafeEnvironmentInjector
	step := &weavev1alpha1.WeaveChainStep{Name: ep.Name, EnvOverrides: ep.Env}
	run := &weavev1alpha1.WeaveRun{
		ObjectMeta: metav1.ObjectMeta{Name: runName, Namespace: app.Namespace},
		Spec:       weavev1alpha1.WeaveRunSpec{ChainRef: corev1.LocalObjectReference{Name: app.Name}},
	}
	job := jobbuilder.Build(tmpl, step, run, 0, "", "", r.SecurityDefaults, "", csMeta, csVersion,
		r.FusionIndexURL, r.LoaderImage, r.WritablePaths, unsafeEnv, extRef, extSecretName)
	applyWritablePaths(&job.Spec.Template.Spec, appWritablePaths(r.RunnerWritablePaths, csMeta, &app.Spec))
	job.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	job.Labels[AppLabel] = app.Name
	job.Labels[EntrypointLabel] = ep.Name
	ttlSeconds := defaultExecutionTTL
	if app.Spec.ExecutionTTLSeconds != nil {
		ttlSeconds = *app.Spec.ExecutionTTLSeconds
	}
	job.Spec.TTLSecondsAfterFinished = &ttlSeconds

	if err := r.Create(ctx, job); err != nil && !errors.IsAlreadyExists(err) {
		if extSecretName != "" {
			_ = r.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: extSecretName, Namespace: app.Namespace}})
		}
		return fail("failed to create job: %v", err)
	}
	return exec
}

// syncExecutions moves Running executions to Succeeded/Failed from their Job state.
func (r *WeaveAppReconciler) syncExecutions(ctx context.Context, app *weavev1alpha1.WeaveApp) error {
	var jobs batchv1.JobList
	if err := r.List(ctx, &jobs, client.InNamespace(app.Namespace), client.MatchingLabels{AppLabel: app.Name}); err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}
	byName := make(map[string]*batchv1.Job, len(jobs.Items))
	for i := range jobs.Items {
		byName[jobs.Items[i].Name] = &jobs.Items[i]
	}

	for i := range app.Status.Executions {
		e := &app.Status.Executions[i]
		if e.Phase != weavev1alpha1.WeaveAppExecutionRunning {
			continue
		}
		job, found := byName[e.JobName]
		now := metav1.Now()
		switch {
		case !found && e.StartTime != nil && time.Since(e.StartTime.Time) < time.Minute:
			continue // the informer cache may not have seen the just-created Job yet
		case !found:
			e.Phase, e.CompletionTime, e.Message = weavev1alpha1.WeaveAppExecutionFailed, &now, "job no longer exists"
		case isJobSucceeded(job):
			e.Phase, e.CompletionTime = weavev1alpha1.WeaveAppExecutionSucceeded, completionOrNow(job)
		case isJobFailed(job):
			e.Phase, e.CompletionTime = weavev1alpha1.WeaveAppExecutionFailed, completionOrNow(job)
			e.Message = jobFailureMessage(ctx, r, r.KubeClient, app.Namespace, job)
		default:
			continue
		}
		if app.Spec.ExternalAuthRef != nil {
			// The token is only needed while the pod runs; owner-ref GC covers failures here.
			_ = r.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name: jobbuilder.ExternalAuthSecretName(e.JobName), Namespace: app.Namespace}})
		}
	}
	return nil
}

func completionOrNow(job *batchv1.Job) *metav1.Time {
	if job.Status.CompletionTime != nil {
		return job.Status.CompletionTime
	}
	now := metav1.Now()
	return &now
}

// ---- scheduling ----

func scheduleKey(nn types.NamespacedName, entrypoint string) string {
	return nn.String() + "/" + entrypoint
}

// syncSchedules makes the cron registrations match app.Spec.Entrypoints. Only
// changed schedules are re-registered so a reconcile never resets a pending tick.
func (r *WeaveAppReconciler) syncSchedules(app *weavev1alpha1.WeaveApp) {
	nn := client.ObjectKeyFromObject(app)
	r.mu.Lock()
	defer r.mu.Unlock()

	want := map[string]string{}
	for _, ep := range app.Spec.Entrypoints {
		if ep.Schedule != "" {
			want[ep.Name] = ep.Schedule
		}
	}
	have := r.registered[nn]
	for name := range have {
		if _, ok := want[name]; !ok {
			r.cron.Remove(scheduleKey(nn, name))
			delete(have, name)
		}
	}
	if have == nil {
		have = map[string]string{}
		r.registered[nn] = have
	}
	for name, sched := range want {
		if have[name] == sched {
			continue
		}
		name := name
		if err := r.cron.Upsert(scheduleKey(nn, name), nn.Namespace, nn.Name, sched, func() { r.fire(nn, name) }); err != nil {
			// validate() already parsed the schedule; this is unreachable in practice.
			continue
		}
		have[name] = sched
	}
}

func (r *WeaveAppReconciler) unregisterSchedules(nn types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.registered[nn] {
		r.cron.Remove(scheduleKey(nn, name))
	}
	delete(r.registered, nn)
	delete(r.pending, nn)
}

// fire runs on the cron goroutine: record the tick and wake the reconciler.
func (r *WeaveAppReconciler) fire(nn types.NamespacedName, entrypoint string) {
	r.mu.Lock()
	if r.pending[nn] == nil {
		r.pending[nn] = map[string]struct{}{}
	}
	r.pending[nn][entrypoint] = struct{}{}
	r.mu.Unlock()

	app := &weavev1alpha1.WeaveApp{ObjectMeta: metav1.ObjectMeta{Namespace: nn.Namespace, Name: nn.Name}}
	select {
	case r.wake <- event.GenericEvent{Object: app}:
	default: // channel full: the pending entry is picked up by the next reconcile anyway
	}
}

func (r *WeaveAppReconciler) takePendingFires(nn types.NamespacedName) map[string]struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	fires := r.pending[nn]
	delete(r.pending, nn)
	if fires == nil {
		fires = map[string]struct{}{}
	}
	return fires
}

// Start implements manager.Runnable: it drains cron panic reports (the scheduler
// already unregistered the panicking entry) so they are logged, not lost.
func (r *WeaveAppReconciler) Start(ctx context.Context) error {
	logger := ctrl.Log.WithName("weaveapp-cron")
	for {
		select {
		case <-ctx.Done():
			return nil
		case p := <-r.panicCh:
			logger.Error(fmt.Errorf("%s", p.Reason), "cron schedule panicked and was unregistered", "app", p.Namespace+"/"+p.Name)
		}
	}
}

func (r *WeaveAppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.Add(r); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&weavev1alpha1.WeaveApp{}, builder.WithPredicates(predicate.Or(
			predicate.GenerationChangedPredicate{},
			predicate.AnnotationChangedPredicate{},
		))).
		Owns(&appsv1.Deployment{}).
		Owns(&batchv1.Job{}).
		WatchesRawSource(source.Channel(r.wake, &handler.EnqueueRequestForObject{})).
		Complete(r)
}
