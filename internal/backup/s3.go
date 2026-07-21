// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package backup

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// NewS3Client builds an *s3.Client from region/endpoint-override rules shared with
// fusion-index's internal/storage.NewS3Client, so both projects behave identically
// against the same MinIO/S3-compatible endpoint.
func NewS3Client(ctx context.Context, region, endpointOverride string) (*s3.Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	var opts []func(*s3.Options)
	if endpointOverride != "" {
		ep := endpointOverride
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = &ep
			o.UsePathStyle = true
		})
	}
	return s3.NewFromConfig(awsCfg, opts...), nil
}

// UploadStream uploads body to bucket/key via multipart upload, splitting into parts
// as needed — the gzipped CRD dump's size isn't known upfront since it's streamed
// straight from the k8s List + YAML-marshal pipeline, never buffered to a temp file.
//
// A failed upload leaves no partial object visible under key: S3 only makes a
// multipart upload's data visible on successful completion; manager.Uploader aborts
// automatically on error.
func UploadStream(ctx context.Context, client *s3.Client, bucket, key string, body io.Reader) error {
	uploader := manager.NewUploader(client)
	if _, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   body,
	}); err != nil {
		return fmt.Errorf("s3 upload stream: %w", err)
	}
	return nil
}

// DownloadStream returns a reader for bucket/key — used by restore to stream a
// backup object without buffering it to local disk first.
func DownloadStream(ctx context.Context, client *s3.Client, bucket, key string) (io.ReadCloser, error) {
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 get object: %w", err)
	}
	return out.Body, nil
}

// FindLatestBackupKey returns the most recent object key under prefix, chosen by
// lexicographic order — safe because backup keys embed a fixed-width, zero-padded
// UTC timestamp (see cmd/backup/backup.go), which sorts the same lexicographically
// and chronologically.
func FindLatestBackupKey(ctx context.Context, client *s3.Client, bucket, prefix string) (string, error) {
	var latest string
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("list backups under %s: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			if obj.Key != nil && *obj.Key > latest {
				latest = *obj.Key
			}
		}
	}
	if latest == "" {
		return "", fmt.Errorf("no backups found under %s", prefix)
	}
	return latest, nil
}
