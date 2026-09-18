// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"fmt"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// minTokenRequestExpiration is the Kubernetes API server's hard-enforced floor
// for TokenRequestSpec.ExpirationSeconds (see the "may not specify a duration
// less than 10 minutes" admission error) — confirmed against a real cluster,
// not documented in the Go type itself. A derived TTL below this (e.g. from a
// short ActiveDeadlineSeconds) must be clamped up, or CreateToken is rejected
// outright and no token is ever minted for that job.
const minTokenRequestExpiration = 10 * time.Minute

// mintServiceAccountToken calls the TokenRequest API (serviceaccounts/token
// subresource) for the named ServiceAccount, requesting a token valid for ttl
// (clamped up to minTokenRequestExpiration when shorter). No boundObjectRef is
// set: TTL is the only invalidation mechanism (binding to a specific Pod would
// require minting after that Pod exists, forcing a two-phase flow with the
// Job's pod sitting in ContainerCreating until a later reconcile catches up —
// rejected for resource-pressure reasons at scale).
func (r *WeaveRunReconciler) mintServiceAccountToken(ctx context.Context, ns, saName string, ttl time.Duration) (string, error) {
	if ttl < minTokenRequestExpiration {
		ttl = minTokenRequestExpiration
	}
	expirationSeconds := int64(ttl.Seconds())
	spec := authenticationv1.TokenRequestSpec{ExpirationSeconds: &expirationSeconds}
	if r.ExternalAuthSAAudience != "" {
		spec.Audiences = []string{r.ExternalAuthSAAudience}
	}
	tr, err := r.KubeClient.CoreV1().ServiceAccounts(ns).CreateToken(ctx, saName, &authenticationv1.TokenRequest{
		Spec: spec,
	}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("mint token for serviceaccount %q: %w", saName, err)
	}
	return tr.Status.Token, nil
}
