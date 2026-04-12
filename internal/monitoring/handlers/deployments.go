// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package handlers

import (
	"net/http"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DeploymentsHandler serves GET /monitor/v1/chains/{name}/deployments.
type DeploymentsHandler struct{ Base }

func NewDeploymentsHandler(b Base) *DeploymentsHandler { return &DeploymentsHandler{b} }

// List handles GET /monitor/v1/chains/{name}/deployments — returns all
// apps/v1 Deployments owned by the named WeaveChain
// (label selector: fusion-platform.io/chain=<name>).
func (h *DeploymentsHandler) List(w http.ResponseWriter, r *http.Request) {
	name := nameFromURL(w, r)
	if name == "" {
		return
	}
	key := "chain:deployments:" + name
	if h.cacheGet(w, key) {
		return
	}

	var depList appsv1.DeploymentList
	if err := h.Client.List(r.Context(), &depList,
		client.InNamespace(h.Namespace),
		client.MatchingLabels{"fusion-platform.io/chain": name},
	); err != nil {
		internalError(w, r, err)
		return
	}

	h.Cache.Set(key, depList.Items)
	writeJSON(w, http.StatusOK, depList.Items)
}
