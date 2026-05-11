# Changelog

All notable changes to fusion-weave are documented here.
Format: [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

---

## [Unreleased]

### Added
- `fusion-platform.io/restart-step=<stepName>` annotation on a WeaveRun triggers a rolling restart of the named Deploy-kind step's Deployment (sets `kubectl.kubernetes.io/restartedAt` on the pod template); annotation is consumed after one use.

### Fixed
- Monitor logs endpoint (`GET /monitor/v1/runs/{name}/steps/{step}/logs`) returned 404 for Deploy-kind steps because it only resolved pods via `jobRef`; added `deploymentRef` path that looks up pods via the Deployment's label selector and picks the best Running pod.
- Deploy steps (stepKind: Deploy) incorrectly transitioned to `Succeeded` once the Deployment became Available, causing the WeaveRun to be marked `Succeeded` while the service was still running. Introduced `StepPhaseDeployed` — a non-terminal active phase that satisfies downstream dependency checks (smoketest can still start after the service is ready) but keeps the WeaveRun in `Running` for the lifetime of the service. The controller now polls deployed steps every 30 s and marks them `Failed` if their Deployment is deleted externally.

---

## [0.3.0] — 2026-05-06

### Added
- `seenRuns` deduplication map on `RunsHandler` — prevents double-counting terminal runs in `weave_run_duration_seconds` histogram across polling cycles
- `refreshChainMetrics` triggered from `GET /monitor/v1/runs` cache-miss path — populates Tier-2 chain gauges without a background loop
- `weave_run_duration_seconds` histogram: labeled by chain, trigger, and terminal status
- `RoutePattern`-based HTTP method+route label on `MonitoringMiddleware` (read after `next.ServeHTTP`)

### Changed
- `GET /monitor/v1/runs` is now the single metrics refresh trigger for all Tier-1 run/step gauges and Tier-2 chain gauges
- Prometheus labeled metrics (`GaugeVec`, `HistogramVec`) are only emitted after first label combination is populated — call `GET /monitor/v1/runs` after deploy to seed them

---

## [0.2.1] — 2026-04-30

### Fixed
- Monitoring RBAC: `deployment/fusion-weave/templates/api-role.yaml` missing `batch/jobs`, `apps/deployments`, `events`, `pods`, `pods/log` rules — caused 500s on `/monitor/v1/runs/{name}` when installed via Helm
- `values.yaml`: monitoring disabled by default (`api.monitoring.enabled: false`)

---

## [0.2.0] — 2026-04-12

### Added
- Monitoring API (`internal/monitoring/`) mounted at `/monitor/v1/` — enabled with `MONITORING_ENABLED=true`
- Handlers: `RunsHandler`, `JobsHandler`, `LogsHandler`, `StatsHandler`, `DeploymentsHandler`, `EventsHandler`
- `TTLCache` (`internal/monitoring/cache/`) — in-process cache with configurable TTL; cache key validated with `fieldSelectorRe` to prevent injection
- `Sink` interface + `KafkaSink` / `NoopSink` (`internal/monitoring/logsink/`) — KafkaSink uses stop/done channel pair for safe shutdown and full flush
- Prometheus metrics server on a separate port (`METRICS_ADDR=:9091`, `/metrics`) — no auth middleware
- `ChainStats` endpoint: lists all runs and filters in-process (CRD field selectors on `spec.*` are not server-side)
- `inWindow()` helper: active runs included in stats only when `StartTime.After(cutoff)` to avoid inflating stats with stuck runs
- Pod log fetching via typed `CoreV1().Pods().GetLogs()` client; container always `"job"` per jobbuilder convention
- `Base` handler carrying shared deps; `cacheGet(w, key) bool` DRY helper used by every handler
- Helm: `api.monitoring.enabled`, `api.monitoring.metricsPort`, `api.monitoring.cacheTTL`, `api.monitoring.maxLogLines`, `api.monitoring.kafka.*`
- Raw-YAML: `config/rbac/api-clusterrole.yaml` for SA TokenReview (cluster-scoped); `config/manager/api-server.yaml`
- API documentation (`apidescription.md`), architecture doc (`ARCHITECTURE.md`), install guide (`INSTALL.md`), test guide (`TEST.md`), examples (`EXAMPLES.md`)

---

## [0.1.0] — 2026-04-12

### Added
- Project scaffold: Go module, Makefile, Dockerfile (multi-stage), `.gitignore`
- 5 CRDs (`api/v1alpha1/`): `WeaveJobTemplate`, `WeaveServiceTemplate`, `WeaveChain`, `WeaveTrigger`, `WeaveRun` — deepcopy generated via controller-gen
- Pure-Go DAG engine (`internal/dag/`): `graph.go` + `executor.go`; fully unit-tested, no Kubernetes dependency
- 5 reconcilers (`internal/controller/`): one per CRD; `weaverun_controller.go` is the main execution engine
- `JobBuilder` (`internal/jobbuilder/`): translates `WeaveJobTemplate` + `WeaveChainStep` + `WeaveRun` into `batch/v1` Job specs
- `DeployBuilder` (`internal/deploybuilder/`): builds `apps/v1 Deployment` + Service + optional Ingress for `stepKind: Deploy`
- Trigger system (`internal/trigger/`): cron scheduler (`cron.go`) and webhook HTTP server (`webhook.go`); callbacks use `source.Channel` + `WatchesRawSource` to wake reconciler
- Step output/input passing: `producesOutput` captures JSON stdout to per-run ConfigMap; `consumesOutputFrom` merges and mounts `/weave-input/input.json`; output namespaced by producer to avoid key collisions
- Shared storage per run: opt-in `spec.sharedStorage` on WeaveChain creates a `ReadWriteMany` PVC (`<runName>-shared`) mounted at `/weave-shared` in every job pod; owned by WeaveRun for GC
- Deploy steps: `stepKind: Deploy` creates/rolling-updates stable `<chainName>-<stepName>` Deployment+Service; owner is WeaveChain (survives run deletion); auto-rollback after `unhealthyDuration`
- REST API (`internal/apiserver/`, `cmd/api/`): chi router, CRUD handlers for all 5 CRDs
- Multi-mode auth: API key (Secret with label `fusion-platform.io/api-key=true`), OIDC JWT (lazy JWKS via `sync.Once`), SA TokenReview; roles: `viewer` / `editor` / `admin`
- RBAC middleware: `viewer`=GET, `editor`=GET/POST/PUT/PATCH, `admin`=all including DELETE
- Health routes (`/healthz`, `/readyz`) registered on root router before auth/RBAC middleware
- Helm chart (`deployment/fusion-weave/`): CRDs in `crds/`, operator + API server deployments, namespaced Role + optional ClusterRole for SA auth
- Raw-YAML manifests (`config/`) for fast iteration without Helm overhead
- Sample manifests: pipeline chain, deploy-demo chain, echo job template, nginx service template, cron/ondemand/webhook triggers
