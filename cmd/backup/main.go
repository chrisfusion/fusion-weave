// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Command backup dumps or restores WeaveJobTemplate, WeaveServiceTemplate,
// WeaveChain, and WeaveTrigger specs to/from S3 — never WeaveRun, never .status.
//
// Usage:
//
//	backup            # dump the current namespace's CRDs to S3 (invoked by the
//	                   # daily CronJob, see deployment/fusion-weave/templates/backup-cronjob.yaml)
//	backup restore     # manual, on-demand disaster recovery — not wired to any
//	                   # automatic Helm trigger
package main

import (
	"log/slog"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(weavev1alpha1.AddToScheme(scheme))
}

func main() {
	cfg := configFromEnv()
	setupLogger(cfg)

	// Build Kubernetes client — no manager, no cache, no leader election: this is
	// a one-shot batch job, not a long-running controller.
	restCfg := ctrl.GetConfigOrDie()
	c, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		slog.Error("unable to create Kubernetes client", "error", err)
		os.Exit(1)
	}

	if len(os.Args) > 1 && os.Args[1] == "restore" {
		runRestore(cfg, c)
		return
	}
	runBackup(cfg, c)
}

func setupLogger(cfg config) {
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
