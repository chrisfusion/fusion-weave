// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package monitoring

import (
	"github.com/go-chi/chi/v5"

	"fusion-platform.io/fusion-weave/internal/monitoring/cache"
	"fusion-platform.io/fusion-weave/internal/monitoring/handlers"
)

// RegisterRoutes mounts all /monitor/v1/* handlers onto r.
// r must already have Auth and RBAC middleware applied — the routes inherit them.
// A single TTL cache is created here and shared across all handlers.
func RegisterRoutes(r chi.Router, cfg Config) {
	ch := cache.New[string, any](cfg.CacheTTL)

	b := handlers.NewBase(
		cfg.Client,
		cfg.KubeClient,
		cfg.Namespace,
		ch,
		cfg.Sink,
		cfg.MaxLogLines,
	)

	runs := handlers.NewRunsHandler(b)
	jobs := handlers.NewJobsHandler(b)
	logs := handlers.NewLogsHandler(b)
	evts := handlers.NewEventsHandler(b)
	deps := handlers.NewDeploymentsHandler(b)
	stats := handlers.NewStatsHandler(b)

	r.Get("/runs", runs.List)
	r.Get("/runs/{name}", runs.Get)
	r.Get("/runs/{name}/jobs", jobs.List)
	r.Get("/runs/{name}/jobs/{jobName}", jobs.Get)
	r.Get("/runs/{name}/steps/{step}/logs", logs.Get)
	r.Get("/runs/{name}/events", evts.ListForRun)
	r.Get("/chains/{name}/deployments", deps.List)
	r.Get("/stats/runs", stats.RunStats)
	r.Get("/stats/chains/{name}", stats.ChainStats)
	r.Get("/events", evts.ListAll)
}
