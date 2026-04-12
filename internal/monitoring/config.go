// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package monitoring implements the fusion-weave monitoring API (/monitor/v1).
package monitoring

import (
	"time"

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"fusion-platform.io/fusion-weave/internal/monitoring/logsink"
)

// Config carries all dependencies required to run the monitoring API.
type Config struct {
	// Namespace is the Kubernetes namespace the operator manages.
	Namespace string

	// Client is the controller-runtime client used for CRD and resource reads.
	Client client.Client

	// KubeClient is the typed Kubernetes client used for pod logs, events,
	// and other core-API resources not handled by the CRD client.
	KubeClient kubernetes.Interface

	// CacheTTL is the TTL applied to all monitoring cache entries.
	CacheTTL time.Duration

	// MaxLogLines is the maximum number of tail lines returned per step log request.
	MaxLogLines int

	// Sink receives log snapshots for external delivery (e.g. Kafka).
	// Use logsink.NoopSink when no sink is configured.
	Sink logsink.Sink
}
