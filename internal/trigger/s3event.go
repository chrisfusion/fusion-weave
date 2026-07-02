// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package trigger

import (
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// s3EventPayload mirrors the MinIO Kafka notification envelope.
type s3EventPayload struct {
	EventName string     `json:"EventName"`
	Records   []s3Record `json:"Records"`
}

type s3Record struct {
	EventName string    `json:"eventName"`
	EventTime string    `json:"eventTime"`
	S3        s3Details `json:"s3"`
}

type s3Details struct {
	Bucket s3Bucket `json:"bucket"`
	Object s3Object `json:"object"`
}

type s3Bucket struct {
	Name string `json:"name"`
}

type s3Object struct {
	Key  string `json:"key"`
	Size int64  `json:"size"`
	ETag string `json:"eTag"`
}

// parseS3EventEnvVars parses a MinIO S3 Kafka event payload and applies filters.
// Returns (envVars, true) when the event passes all filters, (nil, false) otherwise.
// raw is always committed by the caller regardless of the return value.
func parseS3EventEnvVars(raw []byte, eventFilter, bucketFilter []string) ([]corev1.EnvVar, bool) {
	var payload s3EventPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	if len(payload.Records) == 0 {
		return nil, false
	}

	rec := payload.Records[0]
	eventName := payload.EventName
	if eventName == "" {
		eventName = rec.EventName
	}

	if len(eventFilter) > 0 && !s3EventMatchesFilter(eventName, eventFilter) {
		return nil, false
	}
	if len(bucketFilter) > 0 && !s3BucketMatchesFilter(rec.S3.Bucket.Name, bucketFilter) {
		return nil, false
	}

	return []corev1.EnvVar{
		{Name: "S3_EVENT_NAME", Value: eventName},
		{Name: "S3_BUCKET", Value: rec.S3.Bucket.Name},
		{Name: "S3_KEY", Value: rec.S3.Object.Key},
		{Name: "S3_SIZE", Value: fmt.Sprintf("%d", rec.S3.Object.Size)},
		{Name: "S3_ETAG", Value: rec.S3.Object.ETag},
		{Name: "S3_EVENT_TIME", Value: rec.EventTime},
		{Name: "S3_EVENT_JSON", Value: string(raw)},
	}, true
}

// s3EventMatchesFilter returns true when eventName starts with the S3 prefix
// corresponding to any accepted short name ("put", "delete", "get").
func s3EventMatchesFilter(eventName string, filter []string) bool {
	for _, f := range filter {
		var prefix string
		switch strings.ToLower(f) {
		case "put":
			prefix = "s3:ObjectCreated"
		case "delete":
			prefix = "s3:ObjectRemoved"
		case "get":
			prefix = "s3:ObjectAccessed"
		default:
			continue
		}
		if strings.HasPrefix(eventName, prefix) {
			return true
		}
	}
	return false
}

func s3BucketMatchesFilter(bucket string, filter []string) bool {
	for _, b := range filter {
		if b == bucket {
			return true
		}
	}
	return false
}
