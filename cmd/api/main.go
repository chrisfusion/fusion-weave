// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Command api is the fusion-weave REST API server.
// It exposes full CRUD for all fusion-weave CRDs and supports API key,
// OIDC, and Kubernetes ServiceAccount authentication.
package main

import (
	"flag"
	"log/slog"
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

	setupLogger(cfg)
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
	slog.Info("fusion-weave API server starting", "addr", cfg.Addr, "namespace", cfg.Namespace)
	if err := srv.Start(ctx); err != nil {
		slog.Error("API server exited with error", "error", err)
		os.Exit(1)
	}
}

func setupLogger(cfg apiserver.Config) {
	var level slog.Level
	unknownLevel := false
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	case "info", "":
		level = slog.LevelInfo
	default:
		level = slog.LevelInfo
		unknownLevel = true
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.LogFormat == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))

	if unknownLevel {
		slog.Warn("unrecognised LOG_LEVEL, defaulting to info", "value", cfg.LogLevel)
	}
}
