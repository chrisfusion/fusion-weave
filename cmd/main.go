// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package main

import (
	"context"
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/controller"
	"fusion-platform.io/fusion-weave/internal/trigger"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(weavev1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr string
		probeAddr   string
		webhookAddr string
		namespace   string
		leaderElect bool
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the health probe endpoint binds to.")
	flag.StringVar(&webhookAddr, "webhook-bind-address", ":9090", "The address the webhook trigger server binds to.")
	flag.StringVar(&namespace, "namespace", "fusion", "Kubernetes namespace the operator manages.")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election for the controller manager.")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("main")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "fusion-platform.io",
		// H3 fix: constrain the informer cache to the operator's namespace so
		// the namespaced RBAC Role is sufficient and no cross-namespace watches occur.
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				namespace: {},
			},
		},
	})
	if err != nil {
		logger.Error(err, "unable to create manager")
		os.Exit(1)
	}

	// Shared fire channel — webhook and cron write here, trigger controller reads.
	fireCh := make(chan trigger.FireRequest, 64)

	// Cron scheduler.
	cronScheduler := trigger.NewCronScheduler()
	defer cronScheduler.Stop()

	// Webhook server with token lookup via Kubernetes Secrets.
	tokenLookup := func(ctx context.Context, namespace, triggerName string) (string, error) {
		return lookupWebhookToken(ctx, mgr.GetClient(), namespace, triggerName)
	}
	webhookServer := trigger.NewWebhookServer(webhookAddr, fireCh, tokenLookup)
	if err := mgr.Add(webhookServer); err != nil {
		logger.Error(err, "unable to add webhook server to manager")
		os.Exit(1)
	}

	// Typed client for pod log access (not provided by the cached controller-runtime client).
	kubeClient, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		logger.Error(err, "unable to create kubernetes client")
		os.Exit(1)
	}

	// Register controllers.
	if err := (&controller.WeaveJobTemplateReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to set up WeaveJobTemplate controller")
		os.Exit(1)
	}

	if err := (&controller.WeaveServiceTemplateReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to set up WeaveServiceTemplate controller")
		os.Exit(1)
	}

	if err := (&controller.WeaveChainReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to set up WeaveChain controller")
		os.Exit(1)
	}

	triggerReconciler := controller.NewWeaveTriggerReconciler(
		mgr.GetClient(), mgr.GetScheme(), cronScheduler, webhookServer, fireCh,
	)
	if err := triggerReconciler.SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to set up WeaveTrigger controller")
		os.Exit(1)
	}

	if err := (&controller.WeaveRunReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		KubeClient: kubeClient,
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to set up WeaveRun controller")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	logger.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "problem running manager")
		os.Exit(1)
	}
}
