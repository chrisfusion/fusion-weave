// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"sigs.k8s.io/controller-runtime/pkg/client"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
)

var (
	// requestsTotal counts monitoring API requests by route pattern and HTTP status class.
	requestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "weave_monitor_requests_total",
			Help: "Total number of requests handled by the monitoring API.",
		},
		[]string{"endpoint", "status"},
	)

	// requestDuration tracks monitoring API handler latency by route pattern.
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

	// runPhaseGauge reports the current WeaveRun count per phase and chain.
	// Reset and re-populated on each GET /monitor/v1/runs cache miss.
	runPhaseGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "weave_runs_by_phase",
			Help: "Current number of WeaveRuns per phase and chain.",
		},
		[]string{"phase", "chain"},
	)

	// runDurationSeconds is a histogram of completed WeaveRun durations.
	// Each terminal run is observed exactly once (deduplicated by RunsHandler.seenRuns).
	// Buckets are sized for minute-scale pipeline durations.
	runDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "weave_run_duration_seconds",
			Help:    "Duration of completed WeaveRuns in seconds.",
			Buckets: []float64{15, 30, 60, 120, 300, 600, 1200, 1800, 3600},
		},
		[]string{"chain", "phase"},
	)

	// stepPhaseTotal reports the current number of WeaveRun steps in each phase, per chain.
	// Reset and re-populated on each GET /monitor/v1/runs cache miss.
	stepPhaseTotal = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "weave_step_phase_total",
			Help: "Current number of WeaveRun steps in each phase, per chain.",
		},
		[]string{"chain", "phase"},
	)

	// stepRetryCount reports the total accumulated retry count across all active runs, per chain.
	// Reset and re-populated on each GET /monitor/v1/runs cache miss.
	stepRetryCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "weave_step_retry_count",
			Help: "Total step retry count across all WeaveRuns, per chain.",
		},
		[]string{"chain"},
	)

	// chainValid reports whether each WeaveChain is currently valid (1=valid, 0=invalid).
	chainValid = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "weave_chain_valid",
			Help: "1 if the WeaveChain passes validation, 0 otherwise.",
		},
		[]string{"chain"},
	)

	// chainStepCount reports the number of steps defined in each WeaveChain.
	chainStepCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "weave_chain_step_count",
			Help: "Number of steps defined in the WeaveChain spec.",
		},
		[]string{"chain"},
	)

	// chainActiveDeployments reports how many Deploy-kind steps are actively managed per chain.
	chainActiveDeployments = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "weave_chain_active_deployments",
			Help: "Number of Deployments currently tracked by the WeaveChain health monitor.",
		},
		[]string{"chain"},
	)

	// deploymentHealth reports the health of each managed Deployment (1=Healthy, 0=Unhealthy/RollingBack).
	deploymentHealth = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "weave_deployment_health",
			Help: "Health of a Deployment managed by a Deploy-kind chain step: 1=Healthy, 0=Unhealthy or rolling back.",
		},
		[]string{"chain", "step"},
	)
)

// statusRecorder wraps http.ResponseWriter to capture the written status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// MonitoringMiddleware records request count and latency for every monitoring API route.
// It reads the chi route pattern after the handler returns so /runs/{name} and
// /runs/{name}/jobs share one label value rather than one per run name.
func MonitoringMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)

		// RoutePattern is populated by chi after routing completes.
		endpoint := chi.RouteContext(r.Context()).RoutePattern()
		status := strconv.Itoa(rec.status/100) + "xx"
		requestsTotal.WithLabelValues(endpoint, status).Inc()
		requestDuration.WithLabelValues(endpoint).Observe(time.Since(start).Seconds())
	})
}

// refreshChainMetrics lists all WeaveChains and updates the Tier-2 chain-level gauges:
// weave_chain_valid, weave_chain_step_count, weave_chain_active_deployments,
// and weave_deployment_health. Silently returns on Kubernetes errors.
func refreshChainMetrics(ctx context.Context, c client.Client, ns string) {
	var list weavev1alpha1.WeaveChainList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return
	}

	chainValid.Reset()
	chainStepCount.Reset()
	chainActiveDeployments.Reset()
	deploymentHealth.Reset()

	for i := range list.Items {
		chain := &list.Items[i]
		name := chain.Name

		valid := 0.0
		if chain.Status.Valid {
			valid = 1.0
		}
		chainValid.WithLabelValues(name).Set(valid)
		chainStepCount.WithLabelValues(name).Set(float64(len(chain.Spec.Steps)))
		chainActiveDeployments.WithLabelValues(name).Set(float64(len(chain.Status.ActiveDeployments)))

		for _, dep := range chain.Status.ActiveDeployments {
			health := 0.0
			if dep.Health == weavev1alpha1.DeployHealthHealthy {
				health = 1.0
			}
			deploymentHealth.WithLabelValues(name, dep.StepName).Set(health)
		}
	}
}
