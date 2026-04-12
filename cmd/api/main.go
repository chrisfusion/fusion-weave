// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Command api is the fusion-weave REST API server.
// It exposes full CRUD for all fusion-weave CRDs and supports API key,
// OIDC, and Kubernetes ServiceAccount authentication.
package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/apiserver"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(weavev1alpha1.AddToScheme(scheme))
}

func main() {
	cfg := configFromFlags()

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("api")

	// Build Kubernetes client — uses in-cluster config when running inside a pod,
	// falls back to ~/.kube/config for local development.
	restCfg := ctrl.GetConfigOrDie()

	c, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		logger.Error(err, "unable to create Kubernetes client")
		os.Exit(1)
	}

	kc, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		logger.Error(err, "unable to create typed Kubernetes client")
		os.Exit(1)
	}

	srv, err := apiserver.New(cfg, c, kc)
	if err != nil {
		logger.Error(err, "unable to create API server")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()
	logger.Info("fusion-weave API server starting", "addr", cfg.Addr, "namespace", cfg.Namespace)
	if err := srv.Start(ctx); err != nil {
		logger.Error(err, "API server exited with error")
		os.Exit(1)
	}
}
