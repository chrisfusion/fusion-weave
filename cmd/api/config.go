// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package main

import (
	"flag"
	"os"

	"fusion-platform.io/fusion-weave/internal/apiserver"
)

func configFromFlags() apiserver.Config {
	var cfg apiserver.Config

	flag.StringVar(&cfg.Addr, "addr", envOrDefault("API_ADDR", ":8082"), "TCP address to listen on.")
	flag.StringVar(&cfg.Namespace, "namespace", envOrDefault("NAMESPACE", "fusion"), "Kubernetes namespace to manage.")

	flag.BoolVar(&cfg.APIKeyEnabled, "auth-apikey", envBool("AUTH_APIKEY"), "Enable API key authentication.")
	flag.BoolVar(&cfg.OIDCEnabled, "auth-oidc", envBool("AUTH_OIDC"), "Enable OIDC JWT authentication.")
	flag.StringVar(&cfg.OIDCIssuerURL, "oidc-issuer-url", os.Getenv("OIDC_ISSUER_URL"), "OIDC provider issuer URL.")
	flag.StringVar(&cfg.OIDCClientID, "oidc-client-id", os.Getenv("OIDC_CLIENT_ID"), "OIDC expected audience / client ID.")
	flag.StringVar(&cfg.OIDCRoleClaim, "oidc-role-claim", envOrDefault("OIDC_ROLE_CLAIM", "fusion-weave-role"), "JWT claim name carrying the role.")
	flag.BoolVar(&cfg.SAAuthEnabled, "auth-sa", envBool("AUTH_SA"), "Enable Kubernetes ServiceAccount token authentication.")
	flag.BoolVar(&cfg.AllowUnauthenticated, "allow-unauthenticated", envBool("ALLOW_UNAUTHENTICATED"), "Skip all auth checks (cluster-internal mode).")

	return cfg
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string) bool {
	v := os.Getenv(key)
	return v == "true" || v == "1" || v == "yes"
}
