// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package main

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"fusion-platform.io/fusion-weave/internal/backup"
)

// runBackup lists the current namespace's WeaveJobTemplate/WeaveServiceTemplate/
// WeaveChain/WeaveTrigger objects, marshals them as a "---"-separated YAML
// stream, gzips it, and streams it directly into S3 as it's produced — no local
// temp file, so backup size isn't bounded by the Job's ephemeral disk. Mirrors
// fusion-index's runBackupDB producer/consumer-goroutine shape.
func runBackup(cfg config, c client.Client) {
	ctx := context.Background()

	if cfg.S3Bucket == "" {
		slog.Error("S3_BUCKET is required")
		os.Exit(1)
	}

	filename := fmt.Sprintf("backup-%s.yaml.gz", time.Now().UTC().Format("20060102T150405Z"))
	key := path.Join(cfg.S3BackupPrefix, filename)

	pr, pw := io.Pipe()
	gz := gzip.NewWriter(pw)

	// dumpErr carries the DumpObjects/gzip-specific failure (if any), sent before
	// the pipe is closed either way — so it's always ready by the time
	// UploadStream below returns, letting us log the actual cause instead of a
	// generic upload error when the dump itself is what really failed.
	dumpErr := make(chan error, 1)
	var objectCount int
	go func() {
		n, err := backup.DumpObjects(ctx, c, cfg.Namespace, gz)
		objectCount = n
		if err != nil {
			dumpErr <- err
			pw.CloseWithError(err)
			return
		}
		if closeErr := gz.Close(); closeErr != nil {
			err := fmt.Errorf("gzip close: %w", closeErr)
			dumpErr <- err
			pw.CloseWithError(err)
			return
		}
		dumpErr <- nil
		pw.Close()
	}()

	s3Client, err := backup.NewS3Client(ctx, cfg.AWSRegion, cfg.S3EndpointOverride)
	if err != nil {
		slog.Error("create S3 client", "error", err)
		os.Exit(1)
	}
	uploadErr := backup.UploadStream(ctx, s3Client, cfg.S3Bucket, key, pr)

	if err := <-dumpErr; err != nil {
		slog.Error("dump CRDs failed", "key", key, "error", err)
		os.Exit(1)
	}
	if uploadErr != nil {
		slog.Error("upload backup", "key", key, "error", uploadErr)
		os.Exit(1)
	}

	slog.Info("CRD backup complete", "bucket", cfg.S3Bucket, "key", key, "objects", objectCount)
}
