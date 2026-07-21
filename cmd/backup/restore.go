// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package main

import (
	"compress/gzip"
	"context"
	"log/slog"
	"os"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"fusion-platform.io/fusion-weave/internal/backup"
)

// runRestore is a manual, on-demand disaster recovery operation — deliberately
// not wired to any automatic Helm trigger (see fusion-index's restore-db for the
// same reasoning: a destructive DR operation should never be one `helm upgrade
// --set` away from accidentally auto-triggering). Restores the backup named by
// RESTORE_BACKUP_KEY, or the most recent one under S3_BACKUP_PREFIX if unset,
// into cfg.Namespace. Refuses to run if the namespace already has any of the 4
// CRD kinds present unless RESTORE_FORCE=true.
func runRestore(cfg config, c client.Client) {
	ctx := context.Background()

	if cfg.S3Bucket == "" {
		slog.Error("S3_BUCKET is required")
		os.Exit(1)
	}

	hasExisting, err := backup.HasExistingObjects(ctx, c, cfg.Namespace)
	if err != nil {
		slog.Error("check target namespace state", "error", err)
		os.Exit(1)
	}
	if hasExisting && !cfg.RestoreForce {
		slog.Error("target namespace already has chains/templates/triggers — refusing to restore; set RESTORE_FORCE=true to overwrite anyway")
		os.Exit(1)
	}

	s3Client, err := backup.NewS3Client(ctx, cfg.AWSRegion, cfg.S3EndpointOverride)
	if err != nil {
		slog.Error("create S3 client", "error", err)
		os.Exit(1)
	}

	key := cfg.RestoreBackupKey
	if key == "" {
		key, err = backup.FindLatestBackupKey(ctx, s3Client, cfg.S3Bucket, cfg.S3BackupPrefix)
		if err != nil {
			slog.Error("find latest backup", "error", err)
			os.Exit(1)
		}
	}

	rc, err := backup.DownloadStream(ctx, s3Client, cfg.S3Bucket, key)
	if err != nil {
		slog.Error("download backup", "key", key, "error", err)
		os.Exit(1)
	}
	defer rc.Close()

	gz, err := gzip.NewReader(rc)
	if err != nil {
		slog.Error("open gzip stream", "key", key, "error", err)
		os.Exit(1)
	}
	defer gz.Close()

	slog.Info("restoring CRDs from backup", "bucket", cfg.S3Bucket, "key", key, "namespace", cfg.Namespace)
	result, err := backup.RestoreObjects(ctx, c, gz, cfg.Namespace)
	if err != nil {
		slog.Error("restore failed", "key", key, "error", err)
		os.Exit(1)
	}

	for _, objErr := range result.Errors {
		slog.Error("restore object failed", "error", objErr)
	}

	slog.Info("CRD restore complete", "key", key, "created", result.Created, "skipped", result.Skipped, "errors", len(result.Errors))
	if len(result.Errors) > 0 {
		os.Exit(1)
	}
}
