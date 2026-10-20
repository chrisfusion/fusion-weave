// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	weavev1alpha1 "fusion-platform.io/fusion-weave/api/v1alpha1"
	"fusion-platform.io/fusion-weave/internal/keycloakclient"
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
	return mintServiceAccountToken(ctx, r.KubeClient, r.ExternalAuthSAAudience, ns, saName, ttl)
}

// mintServiceAccountToken is the receiver-free implementation shared by the
// WeaveRun and WeaveApp reconcilers.
func mintServiceAccountToken(ctx context.Context, kube kubernetes.Interface, audience, ns, saName string, ttl time.Duration) (string, error) {
	if ttl < minTokenRequestExpiration {
		ttl = minTokenRequestExpiration
	}
	expirationSeconds := int64(ttl.Seconds())
	spec := authenticationv1.TokenRequestSpec{ExpirationSeconds: &expirationSeconds}
	if audience != "" {
		spec.Audiences = []string{audience}
	}
	tr, err := kube.CoreV1().ServiceAccounts(ns).CreateToken(ctx, saName, &authenticationv1.TokenRequest{
		Spec: spec,
	}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("mint token for serviceaccount %q: %w", saName, err)
	}
	return tr.Status.Token, nil
}

// mintOIDCToken reads the allowlisted Secret's Keycloak client credentials and
// performs a client_credentials grant to obtain a short-lived access token.
func mintOIDCToken(ctx context.Context, c client.Reader, ns, secretName string) (string, error) {
	var sec corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: secretName}, &sec); err != nil {
		return "", fmt.Errorf("get oidc credentials secret %q: %w", secretName, err)
	}
	tok, err := keycloakclient.MintClientCredentialsToken(ctx, string(sec.Data["tokenURL"]), string(sec.Data["clientId"]), string(sec.Data["clientSecret"]))
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// mintExternalAuthToken mints the token for ref (mode already allowlist-checked).
func mintExternalAuthToken(ctx context.Context, c client.Reader, kube kubernetes.Interface, audience, ns string, ref *weavev1alpha1.WeaveExternalAuthRef, ttl time.Duration) (string, error) {
	switch ref.Mode {
	case weavev1alpha1.ExternalAuthModeServiceAccount:
		return mintServiceAccountToken(ctx, kube, audience, ns, ref.Name, ttl)
	case weavev1alpha1.ExternalAuthModeOIDC:
		return mintOIDCToken(ctx, c, ns, ref.Name)
	}
	return "", fmt.Errorf("externalAuthRef.mode %q must be serviceAccount or oidc", ref.Mode)
}

// checkExternalAuthAllowed validates ref against the deploy-time allowlist for
// its mode. The only validation point for refs set below chain level.
func checkExternalAuthAllowed(ref *weavev1alpha1.WeaveExternalAuthRef, serviceAccounts, oidcSecrets []string) error {
	var allowed []string
	switch ref.Mode {
	case weavev1alpha1.ExternalAuthModeServiceAccount:
		allowed = serviceAccounts
	case weavev1alpha1.ExternalAuthModeOIDC:
		allowed = oidcSecrets
	default:
		return fmt.Errorf("externalAuthRef.mode %q must be serviceAccount or oidc", ref.Mode)
	}
	if !slices.Contains(allowed, ref.Name) {
		return fmt.Errorf("externalAuthRef.name %q is not in the configured allowlist for mode %q", ref.Name, ref.Mode)
	}
	return nil
}
