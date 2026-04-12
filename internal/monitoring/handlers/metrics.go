// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// requestsTotal counts monitoring API requests by endpoint and HTTP status class.
	requestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "weave_monitor_requests_total",
			Help: "Total number of requests handled by the monitoring API.",
		},
		[]string{"endpoint", "status"},
	)

	// requestDuration tracks monitoring API handler latency.
	requestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "weave_monitor_request_duration_seconds",
			Help:    "Monitoring API request latency in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"endpoint"},
	)

	// cacheHitsTotal counts TTL cache hits across all monitoring endpoints.
	cacheHitsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "weave_monitor_cache_hits_total",
		Help: "Total TTL cache hits in the monitoring API.",
	})

	// cacheMissesTotal counts TTL cache misses across all monitoring endpoints.
	cacheMissesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "weave_monitor_cache_misses_total",
		Help: "Total TTL cache misses in the monitoring API.",
	})

	// runPhaseGauge reports the current WeaveRun count per phase.
	// Updated as a side-effect of the /monitor/v1/runs list response.
	runPhaseGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "weave_runs_by_phase",
			Help: "Current number of WeaveRuns per phase.",
		},
		[]string{"phase"},
	)
)

// Suppress "declared but not used" errors for metrics only read by Prometheus.
var (
	_ = requestsTotal
	_ = requestDuration
)
