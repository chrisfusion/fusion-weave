// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package main

import "os"

// config holds environment-driven settings for both the backup and restore
// subcommands. Env var names match fusion-index's backup-db/restore-db
// conventions (S3_BUCKET, S3_BACKUP_PREFIX, AWS_REGION, S3_ENDPOINT_OVERRIDE,
// RESTORE_FORCE, RESTORE_BACKUP_KEY) so both projects are operated the same way.
type config struct {
	Namespace          string
	S3Bucket           string
	S3BackupPrefix     string
	AWSRegion          string
	S3EndpointOverride string
	RestoreForce       bool
	RestoreBackupKey   string
	LogLevel           string
	LogFormat          string
}

func configFromEnv() config {
	return config{
		Namespace:          envOrDefault("NAMESPACE", "fusion"),
		S3Bucket:           os.Getenv("S3_BUCKET"),
		S3BackupPrefix:     os.Getenv("S3_BACKUP_PREFIX"),
		AWSRegion:          envOrDefault("AWS_REGION", "us-east-1"),
		S3EndpointOverride: os.Getenv("S3_ENDPOINT_OVERRIDE"),
		RestoreForce:       envBool("RESTORE_FORCE"),
		RestoreBackupKey:   os.Getenv("RESTORE_BACKUP_KEY"),
		LogLevel:           envOrDefault("LOG_LEVEL", "info"),
		LogFormat:          envOrDefault("LOG_FORMAT", "json"),
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string) bool {
	v := os.Getenv(key)
	return v == "true" || v == "1" || v == "yes"
}
