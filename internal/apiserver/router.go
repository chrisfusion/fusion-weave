// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package apiserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"fusion-platform.io/fusion-weave/internal/apiserver/auth"
	"fusion-platform.io/fusion-weave/internal/apiserver/handlers"
	"fusion-platform.io/fusion-weave/internal/apiserver/middleware"
)

func newRouter(cfg Config, c client.Client, authCfg auth.Config) http.Handler {
	r := chi.NewRouter()

	// Global middleware applied to all routes.
	r.Use(chiMiddleware.RealIP)
	r.Use(middleware.Recovery)
	r.Use(middleware.Logging)

	// Health endpoints — no auth, outside the authenticated sub-router.
	r.Get("/healthz", handlers.Healthz)
	r.Get("/readyz", handlers.Readyz)

	// REST API v1 — auth and RBAC scoped here so health probes are never gated.
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(middleware.Auth(authCfg))
		r.Use(middleware.RBAC)
		registerCRUD(r, "/jobtemplates", handlers.NewJobTemplateHandler(c, cfg.Namespace))
		registerCRUD(r, "/servicetemplates", handlers.NewServiceTemplateHandler(c, cfg.Namespace))
		registerCRUD(r, "/chains", handlers.NewChainHandler(c, cfg.Namespace))
		registerCRUD(r, "/triggers", handlers.NewTriggerHandler(c, cfg.Namespace))
		registerCRUD(r, "/runs", handlers.NewRunHandler(c, cfg.Namespace))
	})

	return r
}

// registerCRUD mounts the standard six CRUD routes for a resource handler.
func registerCRUD(r chi.Router, path string, h handlers.ResourceHandler) {
	r.Get(path, h.List)
	r.Post(path, h.Create)
	r.Get(path+"/{name}", h.Get)
	r.Put(path+"/{name}", h.Update)
	r.Patch(path+"/{name}", h.Patch)
	r.Delete(path+"/{name}", h.Delete)
}
