// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/deploybuilder"
	"fusion-platform.io/fusion-weave/internal/indexclient"
	"fusion-platform.io/fusion-weave/internal/trigger"
)

func newAppReconciler(t *testing.T, objs ...client.Object) *WeaveAppReconciler {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(&weavev1alpha1.WeaveApp{}).Build()
	panicCh := make(chan trigger.TriggerPanic, 4)
	cronScheduler := trigger.NewCronScheduler(panicCh)
	t.Cleanup(cronScheduler.Stop)
	r := NewWeaveAppReconciler(c, testScheme(t), cronScheduler, panicCh)
	r.KubeClient = fakeclientset.NewSimpleClientset()
	r.Ingress = deploybuilder.IngressConfig{HostSuffix: "example.test"}
	return r
}

func jobApp(name string) *weavev1alpha1.WeaveApp {
	return &weavev1alpha1.WeaveApp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fusion", UID: types.UID("uid-" + name)},
		Spec: weavev1alpha1.WeaveAppSpec{
			Type:    weavev1alpha1.WeaveAppTypeJob,
			Image:   "busybox:1.36",
			Command: []string{"echo", "default"},
			Entrypoints: []weavev1alpha1.WeaveAppEntrypoint{
				{Name: "nightly", Command: []string{"echo", "nightly"}, Schedule: "0 0 2 * * *"},
				{Name: "adhoc"},
			},
		},
	}
}

func serviceApp(name string) *weavev1alpha1.WeaveApp {
	return &weavev1alpha1.WeaveApp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fusion", UID: types.UID("uid-" + name)},
		Spec: weavev1alpha1.WeaveAppSpec{
			Type:  weavev1alpha1.WeaveAppTypeService,
			Image: "nginx:1.27",
			Service: &weavev1alpha1.WeaveAppService{
				Ports:       []weavev1alpha1.WeaveServicePort{{Name: "http", Port: 80, TargetPort: 80, Protocol: corev1.ProtocolTCP}},
				IngressName: "site",
			},
		},
	}
}

func reconcileApp(t *testing.T, r *WeaveAppReconciler, name string) weavev1alpha1.WeaveApp {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "fusion", Name: name}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var got weavev1alpha1.WeaveApp
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: name}, &got); err != nil {
		t.Fatalf("get app: %v", err)
	}
	return got
}

func TestWeaveAppValidate(t *testing.T) {
	r := newAppReconciler(t)
	tests := []struct {
		name    string
		mutate  func(a *weavev1alpha1.WeaveApp)
		app     func() *weavev1alpha1.WeaveApp
		wantMsg string
	}{
		{"valid job", func(a *weavev1alpha1.WeaveApp) {}, func() *weavev1alpha1.WeaveApp { return jobApp("a") }, ""},
		{"valid service", func(a *weavev1alpha1.WeaveApp) {}, func() *weavev1alpha1.WeaveApp { return serviceApp("a") }, ""},
		{"job without entrypoints", func(a *weavev1alpha1.WeaveApp) { a.Spec.Entrypoints = nil }, func() *weavev1alpha1.WeaveApp { return jobApp("a") }, "at least one entrypoint"},
		{"job with service block", func(a *weavev1alpha1.WeaveApp) { a.Spec.Service = &weavev1alpha1.WeaveAppService{} }, func() *weavev1alpha1.WeaveApp { return jobApp("a") }, "only supported for type=service"},
		{"duplicate entrypoint", func(a *weavev1alpha1.WeaveApp) { a.Spec.Entrypoints[1].Name = "nightly" }, func() *weavev1alpha1.WeaveApp { return jobApp("a") }, "duplicate entrypoint"},
		{"bad schedule", func(a *weavev1alpha1.WeaveApp) { a.Spec.Entrypoints[0].Schedule = "not cron" }, func() *weavev1alpha1.WeaveApp { return jobApp("a") }, "invalid schedule"},
		{"five-field schedule rejected", func(a *weavev1alpha1.WeaveApp) { a.Spec.Entrypoints[0].Schedule = "0 2 * * *" }, func() *weavev1alpha1.WeaveApp { return jobApp("a") }, "invalid schedule"},
		{"auth not allowlisted", func(a *weavev1alpha1.WeaveApp) {
			a.Spec.ExternalAuthRef = &weavev1alpha1.WeaveExternalAuthRef{Mode: weavev1alpha1.ExternalAuthModeOIDC, Name: "nope"}
		}, func() *weavev1alpha1.WeaveApp { return jobApp("a") }, "allowlist"},
		{"service with entrypoints", func(a *weavev1alpha1.WeaveApp) {
			a.Spec.Entrypoints = []weavev1alpha1.WeaveAppEntrypoint{{Name: "x"}}
		}, func() *weavev1alpha1.WeaveApp { return serviceApp("a") }, "only supported for type=job"},
		{"service with auth", func(a *weavev1alpha1.WeaveApp) {
			a.Spec.ExternalAuthRef = &weavev1alpha1.WeaveExternalAuthRef{Mode: weavev1alpha1.ExternalAuthModeOIDC, Name: "x"}
		}, func() *weavev1alpha1.WeaveApp { return serviceApp("a") }, "only supported for type=job"},
		{"service without ports or codeSource", func(a *weavev1alpha1.WeaveApp) { a.Spec.Service.Ports = nil }, func() *weavev1alpha1.WeaveApp { return serviceApp("a") }, "service.ports is required"},
		{"service without ports but codeSource", func(a *weavev1alpha1.WeaveApp) {
			a.Spec.Service.Ports = nil
			a.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "x", Tag: "stable"}
		}, func() *weavev1alpha1.WeaveApp { return serviceApp("a") }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.app()
			tc.mutate(a)
			got := r.validate(a)
			if tc.wantMsg == "" && got != "" {
				t.Fatalf("expected valid, got %q", got)
			}
			if tc.wantMsg != "" && !strings.Contains(got, tc.wantMsg) {
				t.Fatalf("expected message containing %q, got %q", tc.wantMsg, got)
			}
		})
	}
}

func TestWeaveAppValidate_IngressNeedsHostSuffix(t *testing.T) {
	r := newAppReconciler(t)
	r.Ingress.HostSuffix = ""
	if msg := r.validate(serviceApp("a")); !strings.Contains(msg, "host suffix") {
		t.Fatalf("expected host suffix message, got %q", msg)
	}
}

func TestWeaveAppService_CreatesOwnedObjects(t *testing.T) {
	app := serviceApp("site")
	r := newAppReconciler(t, app)
	got := reconcileApp(t, r, "site")

	if got.Status.Phase != weavev1alpha1.WeaveAppPhasePending {
		t.Errorf("phase: got %q, want Pending (deployment not yet available)", got.Status.Phase)
	}
	if got.Status.DeploymentName != "site-app" {
		t.Errorf("deploymentName: got %q", got.Status.DeploymentName)
	}
	var d appsv1.Deployment
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: "site-app"}, &d); err != nil {
		t.Fatalf("deployment: %v", err)
	}
	var svc corev1.Service
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: "site-app"}, &svc); err != nil {
		t.Fatalf("service: %v", err)
	}
	var ing networkingv1.Ingress
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: "site-app"}, &ing); err != nil {
		t.Fatalf("ingress: %v", err)
	}
	if host := ing.Spec.Rules[0].Host; host != "site.example.test" {
		t.Errorf("ingress host: got %q", host)
	}
	for _, o := range []metav1.Object{&d, &svc, &ing} {
		refs := o.GetOwnerReferences()
		if len(refs) != 1 || refs[0].Kind != "WeaveApp" || refs[0].Name != "site" || refs[0].Controller == nil || !*refs[0].Controller {
			t.Errorf("%T: expected controller ownerRef to WeaveApp/site, got %+v", o, refs)
		}
	}
}

func TestWeaveAppService_ReadyWhenDeploymentAvailable(t *testing.T) {
	app := serviceApp("site")
	r := newAppReconciler(t, app)
	reconcileApp(t, r, "site")

	var d appsv1.Deployment
	key := types.NamespacedName{Namespace: "fusion", Name: "site-app"}
	if err := r.Get(context.Background(), key, &d); err != nil {
		t.Fatal(err)
	}
	d.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	if got := reconcileApp(t, r, "site"); got.Status.Phase != weavev1alpha1.WeaveAppPhaseReady {
		t.Errorf("phase: got %q, want Ready", got.Status.Phase)
	}
}

func TestWeaveAppService_RemovingIngressNameDeletesIngress(t *testing.T) {
	app := serviceApp("site")
	r := newAppReconciler(t, app)
	reconcileApp(t, r, "site")

	var cur weavev1alpha1.WeaveApp
	_ = r.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: "site"}, &cur)
	cur.Spec.Service.IngressName = ""
	if err := r.Update(context.Background(), &cur); err != nil {
		t.Fatal(err)
	}
	reconcileApp(t, r, "site")

	var ing networkingv1.Ingress
	err := r.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: "site-app"}, &ing)
	if err == nil {
		t.Fatal("expected ingress to be deleted")
	}
}

func TestWeaveAppInvalid_SetsPhaseAndNoObjects(t *testing.T) {
	app := jobApp("bad")
	app.Spec.Entrypoints = nil
	r := newAppReconciler(t, app)
	got := reconcileApp(t, r, "bad")
	if got.Status.Phase != weavev1alpha1.WeaveAppPhaseInvalid || got.Status.ValidationMessage == "" {
		t.Errorf("expected Invalid with message, got %+v", got.Status)
	}
}

func listAppJobs(t *testing.T, r *WeaveAppReconciler) []batchv1.Job {
	t.Helper()
	var jobs batchv1.JobList
	if err := r.List(context.Background(), &jobs, client.InNamespace("fusion")); err != nil {
		t.Fatal(err)
	}
	return jobs.Items
}

func fireAnnotated(t *testing.T, r *WeaveAppReconciler, name, entrypoint string) {
	t.Helper()
	var cur weavev1alpha1.WeaveApp
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: name}, &cur); err != nil {
		t.Fatal(err)
	}
	if cur.Annotations == nil {
		cur.Annotations = map[string]string{}
	}
	cur.Annotations[FireAnnotation] = entrypoint
	if err := r.Update(context.Background(), &cur); err != nil {
		t.Fatal(err)
	}
}

func TestWeaveAppJob_FireAnnotationLaunchesOwnedJob(t *testing.T) {
	app := jobApp("etl")
	r := newAppReconciler(t, app)
	reconcileApp(t, r, "etl") // registers schedules
	fireAnnotated(t, r, "etl", "nightly")
	got := reconcileApp(t, r, "etl")

	jobs := listAppJobs(t, r)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	j := jobs[0]
	if j.Labels[AppLabel] != "etl" || j.Labels[EntrypointLabel] != "nightly" {
		t.Errorf("labels: %+v", j.Labels)
	}
	if refs := j.OwnerReferences; len(refs) != 1 || refs[0].Kind != "WeaveApp" || refs[0].Name != "etl" {
		t.Errorf("ownerRefs: %+v", refs)
	}
	if j.Spec.TTLSecondsAfterFinished == nil || *j.Spec.TTLSecondsAfterFinished != defaultExecutionTTL {
		t.Errorf("ttl: %v", j.Spec.TTLSecondsAfterFinished)
	}
	c := j.Spec.Template.Spec.Containers[0]
	if strings.Join(c.Command, " ") != "echo nightly" {
		t.Errorf("entrypoint command should override app command, got %v", c.Command)
	}
	if *j.Spec.Parallelism != 1 || *j.Spec.Completions != 1 {
		t.Errorf("parallelism/completions must be 1")
	}
	if _, ok := got.Annotations[FireAnnotation]; ok {
		t.Error("fire annotation should have been consumed")
	}
	if len(got.Status.Executions) != 1 || got.Status.Executions[0].Phase != weavev1alpha1.WeaveAppExecutionRunning {
		t.Fatalf("executions: %+v", got.Status.Executions)
	}
	if got.Status.Phase != weavev1alpha1.WeaveAppPhaseRunning {
		t.Errorf("phase: %q", got.Status.Phase)
	}
}

func TestWeaveAppJob_EntrypointWithoutCommandUsesAppCommand(t *testing.T) {
	r := newAppReconciler(t, jobApp("etl"))
	reconcileApp(t, r, "etl")
	fireAnnotated(t, r, "etl", "adhoc")
	reconcileApp(t, r, "etl")

	jobs := listAppJobs(t, r)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if got := strings.Join(jobs[0].Spec.Template.Spec.Containers[0].Command, " "); got != "echo default" {
		t.Errorf("got %q", got)
	}
}

func TestWeaveAppJob_OverlappingFireIsSkipped(t *testing.T) {
	r := newAppReconciler(t, jobApp("etl"))
	reconcileApp(t, r, "etl")
	fireAnnotated(t, r, "etl", "adhoc")
	reconcileApp(t, r, "etl")
	fireAnnotated(t, r, "etl", "adhoc")
	got := reconcileApp(t, r, "etl")

	if n := len(listAppJobs(t, r)); n != 1 {
		t.Errorf("expected overlapping fire to be skipped, got %d jobs", n)
	}
	if len(got.Status.Executions) != 1 {
		t.Errorf("executions: %+v", got.Status.Executions)
	}
}

func TestWeaveAppJob_UnknownEntrypointFireIsConsumedAndIgnored(t *testing.T) {
	r := newAppReconciler(t, jobApp("etl"))
	reconcileApp(t, r, "etl")
	fireAnnotated(t, r, "etl", "ghost")
	got := reconcileApp(t, r, "etl")
	if _, ok := got.Annotations[FireAnnotation]; ok {
		t.Error("annotation should be consumed")
	}
	if len(listAppJobs(t, r)) != 0 {
		t.Error("no job expected")
	}
}

func TestWeaveAppJob_CronTickLaunchesJob(t *testing.T) {
	r := newAppReconciler(t, jobApp("etl"))
	reconcileApp(t, r, "etl")

	// Simulate the cron callback instead of waiting for 02:00.
	r.fire(types.NamespacedName{Namespace: "fusion", Name: "etl"}, "nightly")
	select {
	case <-r.wake:
	default:
		t.Fatal("fire should wake the reconciler")
	}
	reconcileApp(t, r, "etl")
	if n := len(listAppJobs(t, r)); n != 1 {
		t.Fatalf("expected 1 job from cron tick, got %d", n)
	}
}

func TestWeaveAppJob_SchedulesFollowSpec(t *testing.T) {
	r := newAppReconciler(t, jobApp("etl"))
	reconcileApp(t, r, "etl")
	nn := types.NamespacedName{Namespace: "fusion", Name: "etl"}
	if got := r.registered[nn]; len(got) != 1 || got["nightly"] == "" {
		t.Fatalf("registered: %+v", got)
	}

	var cur weavev1alpha1.WeaveApp
	_ = r.Get(context.Background(), nn, &cur)
	cur.Spec.Entrypoints[0].Schedule = ""
	if err := r.Update(context.Background(), &cur); err != nil {
		t.Fatal(err)
	}
	reconcileApp(t, r, "etl")
	if got := r.registered[nn]; len(got) != 0 {
		t.Fatalf("schedule should be unregistered, got %+v", got)
	}
}

func TestWeaveAppJob_SyncExecutionsFromJobState(t *testing.T) {
	r := newAppReconciler(t, jobApp("etl"))
	reconcileApp(t, r, "etl")
	fireAnnotated(t, r, "etl", "adhoc")
	reconcileApp(t, r, "etl")

	jobs := listAppJobs(t, r)
	j := jobs[0]
	j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	got := reconcileApp(t, r, "etl")
	if e := got.Status.Executions[0]; e.Phase != weavev1alpha1.WeaveAppExecutionSucceeded || e.CompletionTime == nil {
		t.Errorf("execution: %+v", e)
	}
	if got.Status.Phase != weavev1alpha1.WeaveAppPhaseReady {
		t.Errorf("phase: %q", got.Status.Phase)
	}
}

func TestWeaveAppJob_VanishedJobMarkedFailedAfterGracePeriod(t *testing.T) {
	r := newAppReconciler(t, jobApp("etl"))
	reconcileApp(t, r, "etl")
	fireAnnotated(t, r, "etl", "adhoc")
	reconcileApp(t, r, "etl")

	// Within the grace period a missing Job is treated as informer lag.
	for _, j := range listAppJobs(t, r) {
		j := j
		_ = r.Delete(context.Background(), &j)
	}
	if got := reconcileApp(t, r, "etl"); got.Status.Executions[0].Phase != weavev1alpha1.WeaveAppExecutionRunning {
		t.Fatalf("expected Running within grace period, got %+v", got.Status.Executions[0])
	}

	var cur weavev1alpha1.WeaveApp
	_ = r.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: "etl"}, &cur)
	old := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	cur.Status.Executions[0].StartTime = &old
	if err := r.Status().Update(context.Background(), &cur); err != nil {
		t.Fatal(err)
	}
	if got := reconcileApp(t, r, "etl"); got.Status.Executions[0].Phase != weavev1alpha1.WeaveAppExecutionFailed {
		t.Fatalf("expected Failed after grace period, got %+v", got.Status.Executions[0])
	}
}

func TestWeaveAppJob_ExternalAuthCreatesOwnedSecretAndMount(t *testing.T) {
	app := jobApp("etl")
	app.Spec.ExternalAuthRef = &weavev1alpha1.WeaveExternalAuthRef{Mode: weavev1alpha1.ExternalAuthModeServiceAccount, Name: "etl-sa"}
	r := newAppReconciler(t, app)
	r.ExternalAuthServiceAccounts = []string{"etl-sa"}
	r.ExternalAuthDefaultTTL = 15 * time.Minute
	clientset := fakeclientset.NewSimpleClientset()
	clientset.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(k8stesting.CreateActionImpl)
		if !ok || ca.GetSubresource() != "token" {
			return false, nil, nil
		}
		return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{Token: "tok"}}, nil
	})
	r.KubeClient = clientset

	reconcileApp(t, r, "etl")
	fireAnnotated(t, r, "etl", "adhoc")
	reconcileApp(t, r, "etl")

	jobs := listAppJobs(t, r)
	if len(jobs) != 1 {
		t.Fatalf("expected a job, got %d", len(jobs))
	}
	secretName := jobs[0].Name + "-ext-auth"
	var sec corev1.Secret
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: secretName}, &sec); err != nil {
		t.Fatalf("secret: %v", err)
	}
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].Name != "etl" {
		t.Errorf("secret ownerRefs: %+v", sec.OwnerReferences)
	}
	found := false
	for _, v := range jobs[0].Spec.Template.Spec.Volumes {
		if v.Secret != nil && v.Secret.SecretName == secretName {
			found = true
		}
	}
	if !found {
		t.Error("job should mount the external auth secret")
	}

	// Terminal job: the token Secret is removed.
	j := jobs[0]
	j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	reconcileApp(t, r, "etl")
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: secretName}, &sec); err == nil {
		t.Error("secret should be deleted once the execution finished")
	}
}

func TestWeaveAppJob_MintFailureRecordedAsFailedExecution(t *testing.T) {
	app := jobApp("etl")
	app.Spec.ExternalAuthRef = &weavev1alpha1.WeaveExternalAuthRef{Mode: weavev1alpha1.ExternalAuthModeServiceAccount, Name: "etl-sa"}
	r := newAppReconciler(t, app)
	r.ExternalAuthServiceAccounts = []string{"etl-sa"}
	clientset := fakeclientset.NewSimpleClientset()
	clientset.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, context.DeadlineExceeded
	})
	r.KubeClient = clientset

	reconcileApp(t, r, "etl")
	fireAnnotated(t, r, "etl", "adhoc")
	got := reconcileApp(t, r, "etl")

	if len(listAppJobs(t, r)) != 0 {
		t.Error("no job expected when minting fails")
	}
	if len(got.Status.Executions) != 1 || got.Status.Executions[0].Phase != weavev1alpha1.WeaveAppExecutionFailed ||
		!strings.Contains(got.Status.Executions[0].Message, "mint external auth token") {
		t.Errorf("executions: %+v", got.Status.Executions)
	}
}

func TestWeaveAppJob_ExecutionHistoryCapped(t *testing.T) {
	r := newAppReconciler(t, jobApp("etl"))
	reconcileApp(t, r, "etl")

	var cur weavev1alpha1.WeaveApp
	nn := types.NamespacedName{Namespace: "fusion", Name: "etl"}
	_ = r.Get(context.Background(), nn, &cur)
	for i := 0; i < maxAppExecutions+5; i++ {
		cur.Status.Executions = append(cur.Status.Executions, weavev1alpha1.WeaveAppExecution{
			Entrypoint: "adhoc", JobName: "old", Phase: weavev1alpha1.WeaveAppExecutionSucceeded})
	}
	if err := r.Status().Update(context.Background(), &cur); err != nil {
		t.Fatal(err)
	}
	fireAnnotated(t, r, "etl", "adhoc")
	got := reconcileApp(t, r, "etl")
	if len(got.Status.Executions) != maxAppExecutions {
		t.Errorf("executions: got %d, want %d", len(got.Status.Executions), maxAppExecutions)
	}
	if got.Status.Executions[0].Phase != weavev1alpha1.WeaveAppExecutionRunning {
		t.Error("newest execution should be first")
	}
}

func TestWeaveAppDeleted_UnregistersSchedules(t *testing.T) {
	r := newAppReconciler(t, jobApp("etl"))
	reconcileApp(t, r, "etl")
	nn := types.NamespacedName{Namespace: "fusion", Name: "etl"}
	if len(r.registered[nn]) == 0 {
		t.Fatal("precondition: schedule registered")
	}
	var cur weavev1alpha1.WeaveApp
	_ = r.Get(context.Background(), nn, &cur)
	_ = r.Delete(context.Background(), &cur)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: nn}); err != nil {
		t.Fatal(err)
	}
	if len(r.registered[nn]) != 0 {
		t.Error("schedules should be unregistered after delete")
	}
}

func getAppIngress(t *testing.T, r *WeaveAppReconciler, name string) networkingv1.Ingress {
	t.Helper()
	var ing networkingv1.Ingress
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: name}, &ing); err != nil {
		t.Fatalf("ingress: %v", err)
	}
	return ing
}

func TestWeaveAppService_FullIngressKeepsPathTypeClassAndTLS(t *testing.T) {
	app := serviceApp("site")
	class := "nginx"
	app.Spec.Service.IngressName = ""
	app.Spec.Service.Ingress = &weavev1alpha1.WeaveAppIngress{
		IngressClassName: &class,
		TLSSecretName:    "site-cert",
		Rules: []weavev1alpha1.WeaveAppIngressRule{
			{Name: "site", Path: "/app", PathType: "Exact", ServicePort: "http"},
			{Name: "alias", Path: "/api", PathType: "ImplementationSpecific"},
		},
	}
	r := newAppReconciler(t, app)
	reconcileApp(t, r, "site")
	ing := getAppIngress(t, r, "site-app")

	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "nginx" {
		t.Errorf("ingressClassName: %v", ing.Spec.IngressClassName)
	}
	if len(ing.Spec.Rules) != 2 {
		t.Fatalf("rules: %d", len(ing.Spec.Rules))
	}
	p0 := ing.Spec.Rules[0].HTTP.Paths[0]
	if ing.Spec.Rules[0].Host != "site.example.test" || p0.Path != "/app" || *p0.PathType != networkingv1.PathTypeExact {
		t.Errorf("rule 0: host=%s path=%s type=%v", ing.Spec.Rules[0].Host, p0.Path, *p0.PathType)
	}
	p1 := ing.Spec.Rules[1].HTTP.Paths[0]
	if ing.Spec.Rules[1].Host != "alias.example.test" || p1.Path != "/api" || *p1.PathType != networkingv1.PathTypeImplementationSpecific {
		t.Errorf("rule 1: %+v", ing.Spec.Rules[1])
	}
	if p1.Backend.Service.Port.Name != "http" {
		t.Errorf("empty servicePort should default to the first port, got %+v", p1.Backend.Service.Port)
	}
	if len(ing.Spec.TLS) != 1 || ing.Spec.TLS[0].SecretName != "site-cert" || len(ing.Spec.TLS[0].Hosts) != 2 {
		t.Errorf("tls: %+v", ing.Spec.TLS)
	}
}

func TestWeaveAppService_InstanceWideIngressConfigApplied(t *testing.T) {
	r := newAppReconciler(t, serviceApp("site"))
	r.Ingress.Annotations = map[string]string{"nginx.ingress.kubernetes.io/proxy-body-size": "50m"}
	r.Ingress.Labels = map[string]string{"team": "platform"}
	r.Ingress.TLS = true
	reconcileApp(t, r, "site")
	ing := getAppIngress(t, r, "site-app")

	if ing.Annotations["nginx.ingress.kubernetes.io/proxy-body-size"] != "50m" {
		t.Errorf("annotations: %+v", ing.Annotations)
	}
	if ing.Labels["team"] != "platform" {
		t.Errorf("labels: %+v", ing.Labels)
	}
	if len(ing.Spec.TLS) != 1 || ing.Spec.TLS[0].SecretName != "site-app-tls" {
		t.Errorf("tls: %+v", ing.Spec.TLS)
	}
}

func TestWeaveAppService_ChangedIngressConfigReachesExistingIngress(t *testing.T) {
	r := newAppReconciler(t, serviceApp("site"))
	r.Ingress.Annotations = map[string]string{"a": "1"}
	reconcileApp(t, r, "site")

	// A foreign annotation must survive, a changed configured one must update.
	ing := getAppIngress(t, r, "site-app")
	ing.Annotations["foreign"] = "keep"
	if err := r.Update(context.Background(), &ing); err != nil {
		t.Fatal(err)
	}
	r.Ingress.Annotations = map[string]string{"a": "2"}
	var cur weavev1alpha1.WeaveApp
	_ = r.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: "site"}, &cur)
	reconcileApp(t, r, "site")

	ing = getAppIngress(t, r, "site-app")
	if ing.Annotations["a"] != "2" || ing.Annotations["foreign"] != "keep" {
		t.Errorf("annotations: %+v", ing.Annotations)
	}
}

func TestWeaveAppService_IngressNameAndIngressMutuallyExclusive(t *testing.T) {
	app := serviceApp("site")
	app.Spec.Service.Ingress = &weavev1alpha1.WeaveAppIngress{Rules: []weavev1alpha1.WeaveAppIngressRule{{Name: "x"}}}
	r := newAppReconciler(t)
	if msg := r.validate(app); !strings.Contains(msg, "mutually exclusive") {
		t.Fatalf("got %q", msg)
	}
}

func TestWeaveAppService_IngressRuleUnknownServicePortRejected(t *testing.T) {
	app := serviceApp("site")
	app.Spec.Service.IngressName = ""
	app.Spec.Service.Ingress = &weavev1alpha1.WeaveAppIngress{Rules: []weavev1alpha1.WeaveAppIngressRule{{Name: "x", ServicePort: "nope"}}}
	r := newAppReconciler(t)
	if msg := r.validate(app); !strings.Contains(msg, "not declared") {
		t.Fatalf("got %q", msg)
	}
	app.Spec.Service.Ingress.Rules[0].ServicePort = "8080" // numeric is always accepted
	if msg := r.validate(app); msg != "" {
		t.Fatalf("numeric servicePort should be valid, got %q", msg)
	}
}

func TestIngressSpecFor_DefaultsToRunnerPortFromMetadata(t *testing.T) {
	meta := &indexclient.AppMetadata{}
	meta.Runner.Port = 8501
	spec := ingressSpecFor(&weavev1alpha1.WeaveAppService{IngressName: "dash"}, meta)
	if spec == nil || len(spec.Rules) != 1 || spec.Rules[0].ServicePort != "8501" || spec.Rules[0].Path != "/" {
		t.Fatalf("spec: %+v", spec)
	}
	if ingressSpecFor(&weavev1alpha1.WeaveAppService{}, nil) != nil {
		t.Error("no ingress configured must yield nil")
	}
}

func TestWeaveAppService_CodeSourceMetadataDrivesIngressPathAndPort(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":42}]}`))
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"major":1,"minor":2,"patch":3,"tags":[{"tag":"stable"}]}]`))
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions/1.2.3/files", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":7,"name":"metadata.yaml"}]`))
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions/1.2.3/files/7/download", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("runner:\n  type: streamlit\n  port: 8501\ningress:\n  path: /dash\n"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	app := serviceApp("dash")
	app.Spec.Service = &weavev1alpha1.WeaveAppService{IngressName: "dash"} // no ports, no path
	app.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "myapp", Tag: "stable", IndexURL: ts.URL}
	r := newAppReconciler(t, app)
	got := reconcileApp(t, r, "dash")

	ing := getAppIngress(t, r, "dash-app")
	path := ing.Spec.Rules[0].HTTP.Paths[0]
	if path.Path != "/dash" {
		t.Errorf("ingress path should follow metadata ingress.path, got %q", path.Path)
	}
	if path.Backend.Service.Port.Number != 8501 {
		t.Errorf("ingress port should follow metadata runner.port, got %+v", path.Backend.Service.Port)
	}
	var svc corev1.Service
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: "dash-app"}, &svc); err != nil {
		t.Fatal(err)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 8501 {
		t.Errorf("service ports: %+v", svc.Spec.Ports)
	}
	if got.Status.CodeSourceDeployedVersion != "1.2.3" {
		t.Errorf("deployed version: %q", got.Status.CodeSourceDeployedVersion)
	}
}

func TestWeaveAppValidate_ImagePolicy(t *testing.T) {
	r := newAppReconciler(t)
	r.AllowedImagePrefixes = []string{"reg.io/team/"}

	app := jobApp("a")
	app.Spec.Image = "reg.io/team/app:1.0"
	if msg := r.validate(app); msg != "" {
		t.Fatalf("allowed image rejected: %q", msg)
	}
	app.Spec.Image = "evil.io/app:1.0"
	if msg := r.validate(app); !strings.Contains(msg, "spec.image") {
		t.Fatalf("foreign image accepted: %q", msg)
	}
	app.Spec.Image = "reg.io/team/app:1.0"
	app.Spec.CodeSource = &weavev1alpha1.CodeSourceSpec{ArtifactName: "x", Tag: "t", LoaderImage: "evil.io/loader:1"}
	if msg := r.validate(app); !strings.Contains(msg, "loaderImage") {
		t.Fatalf("foreign loader image accepted: %q", msg)
	}
	app.Spec.CodeSource.LoaderImage = ""
	if msg := r.validate(app); msg != "" {
		t.Fatalf("operator-default loader must not be policy-checked: %q", msg)
	}
	r.AllowedImagePrefixes = nil
	app.Spec.Image = "anything"
	if msg := r.validate(app); msg != "" {
		t.Fatalf("no policy configured must allow: %q", msg)
	}
}

func TestWeaveAppImagePolicy_InvalidAppKeepsRunningDeployment(t *testing.T) {
	app := serviceApp("site")
	r := newAppReconciler(t, app)
	reconcileApp(t, r, "site")

	r.AllowedImagePrefixes = []string{"reg.io/team/"} // policy tightened after the app was admitted
	got := reconcileApp(t, r, "site")
	if got.Status.Phase != weavev1alpha1.WeaveAppPhaseInvalid || !strings.Contains(got.Status.ValidationMessage, "spec.image") {
		t.Fatalf("status: %+v", got.Status)
	}
	var d appsv1.Deployment
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "fusion", Name: "site-app"}, &d); err != nil {
		t.Fatalf("existing deployment must stay untouched: %v", err)
	}
	if d.Spec.Template.Spec.Containers[0].Image != "nginx:1.27" {
		t.Errorf("image changed: %s", d.Spec.Template.Spec.Containers[0].Image)
	}
}

func TestWeaveAppImagePolicy_RejectedImageNeverLaunchesJob(t *testing.T) {
	app := jobApp("etl")
	r := newAppReconciler(t, app)
	r.AllowedImagePrefixes = []string{"reg.io/team/"}
	reconcileApp(t, r, "etl")
	fireAnnotated(t, r, "etl", "adhoc")
	reconcileApp(t, r, "etl")
	if n := len(listAppJobs(t, r)); n != 0 {
		t.Fatalf("no job may run with a rejected image, got %d", n)
	}
}

func TestWeaveAppService_IngressAnnotationsExtendAndOverrideInstanceWide(t *testing.T) {
	app := serviceApp("site")
	app.Spec.Service.IngressAnnotations = map[string]string{
		"traefik.ingress.kubernetes.io/router.middlewares": "fusion-headers@kubernetescrd",
		"shared": "app",
	}
	r := newAppReconciler(t, app)
	r.Ingress.Annotations = map[string]string{"shared": "instance", "cert-manager.io/cluster-issuer": "le"}
	reconcileApp(t, r, "site")
	ing := getAppIngress(t, r, "site-app")

	want := map[string]string{
		"traefik.ingress.kubernetes.io/router.middlewares": "fusion-headers@kubernetescrd",
		"shared":                         "app",
		"cert-manager.io/cluster-issuer": "le",
	}
	for k, v := range want {
		if ing.Annotations[k] != v {
			t.Errorf("annotation %s = %q, want %q (all: %+v)", k, ing.Annotations[k], v, ing.Annotations)
		}
	}
}

func TestOverlayAppIngressAnnotations_LayerOrder(t *testing.T) {
	cfg := deploybuilder.IngressConfig{RunnerAnnotations: map[string]map[string]string{
		"streamlit": {"timeout": "runner", "runner-only": "x"},
		"fastapi":   {"other": "y"},
	}}
	svc := &weavev1alpha1.WeaveAppService{IngressAnnotations: map[string]string{"timeout": "app"}}
	meta := &indexclient.AppMetadata{Runner: indexclient.AppRunner{Type: "streamlit"}}

	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"timeout": "instance", "base": "b"}}}
	overlayAppIngressAnnotations(ing, cfg, meta, svc)
	if ing.Annotations["timeout"] != "app" || ing.Annotations["runner-only"] != "x" || ing.Annotations["base"] != "b" {
		t.Errorf("annotations: %+v", ing.Annotations)
	}
	if _, ok := ing.Annotations["other"]; ok {
		t.Errorf("another runner type's annotations leaked: %+v", ing.Annotations)
	}

	// No metadata (no codeSource): only the app's own annotations apply.
	ing = &networkingv1.Ingress{}
	overlayAppIngressAnnotations(ing, cfg, nil, svc)
	if len(ing.Annotations) != 1 || ing.Annotations["timeout"] != "app" {
		t.Errorf("annotations without metadata: %+v", ing.Annotations)
	}
}

func TestWeaveAppService_IngressAnnotationsWithoutIngressRejected(t *testing.T) {
	app := serviceApp("site")
	app.Spec.Service.IngressName = ""
	app.Spec.Service.IngressAnnotations = map[string]string{"a": "b"}
	r := newAppReconciler(t)
	if msg := r.validate(app); !strings.Contains(msg, "ingressAnnotations requires") {
		t.Fatalf("got %q", msg)
	}
}
