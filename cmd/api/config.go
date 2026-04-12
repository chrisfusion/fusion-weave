// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package main

import (
	"flag"
	"os"
	"strconv"
	"time"

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

	flag.BoolVar(&cfg.MonitoringEnabled, "monitoring", envBool("MONITORING_ENABLED"), "Enable /monitor/v1 routes.")
	flag.StringVar(&cfg.MetricsAddr, "metrics-addr", envOrDefault("METRICS_ADDR", ":9091"), "TCP address for the Prometheus metrics server (empty to disable).")
	flag.DurationVar(&cfg.MonitorCacheTTL, "monitor-cache-ttl", envDuration("MONITOR_CACHE_TTL", 30*time.Second), "TTL for monitoring in-memory cache entries.")
	flag.IntVar(&cfg.MonitorMaxLogLines, "monitor-log-lines", envInt("MONITOR_LOG_LINES", 100), "Maximum tail log lines returned per step.")

	flag.BoolVar(&cfg.KafkaEnabled, "kafka", envBool("KAFKA_ENABLED"), "Enable Kafka log sink.")
	flag.StringVar(&cfg.KafkaBrokers, "kafka-brokers", os.Getenv("KAFKA_BROKERS"), "Comma-separated Kafka broker addresses.")
	flag.StringVar(&cfg.KafkaTopic, "kafka-topic", envOrDefault("KAFKA_TOPIC", "weave-logs"), "Kafka topic for log snapshots.")

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

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
