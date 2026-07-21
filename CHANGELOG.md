# Changelog

All notable changes to fusion-weave are documented here.
Format: [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

---

## [Unreleased]

### Added
- `cmd/backup` — new `/backup` binary (`backup`/`restore` subcommands) for disaster recovery. Daily CronJob dumps all `WeaveJobTemplate`/`WeaveServiceTemplate`/`WeaveChain`/`WeaveTrigger` specs in the namespace (never `WeaveRun`, never `.status`) as a single gzipped multi-document YAML file to S3, streamed without a local temp file. Env var conventions (`S3_BUCKET`, `S3_BACKUP_PREFIX`, `AWS_REGION`, `S3_ENDPOINT_OVERRIDE`) mirror fusion-index's `backup-db`/`restore-db`. Restore is manual-only (`kubectl run --rm -i ... -- /backup restore`), not chart-templated, and refuses to overwrite a namespace that already has any of the 4 kinds present unless `RESTORE_FORCE=true`. Gated by `backup.enabled` in Helm (`backup-cronjob.yaml`), backed by a dedicated read-only `fusion-weave-backup` ServiceAccount/Role (no write access, excludes `weaveruns`). Known gap: restoring a chain's spec does not recreate a live Deploy-kind step's Deployment — only a `WeaveRun` executing that step does that, so post-restore re-firing of deploy chains is a manual step.
- `WeaveJobTemplateSpec.codeSource` — Job-kind steps can now reference a versioned fusion-index artifact by tag, using the same `CodeSourceSpec` and `code-loader` init container mechanism as Deploy-kind steps' `WeaveServiceTemplateSpec.codeSource`. No polling or rolling-restart is needed: every `WeaveRun` creates a fresh Job pod, so the tag is re-resolved to its current version on each run.
- `internal/codesource` — new package holding the `WEAVE_*` env var construction and writable-path volume-naming helpers shared by `deploybuilder` and `jobbuilder`, relocated out of `deploybuilder` (previously private) to avoid duplicating the logic for the new Job-step codeSource support.
- `WeaveChainSpec.authSecretRef` — optionally names a Secret injected via `envFrom` into every step pod (Job and Deploy kind) of the chain, so runner-side helper libraries (e.g. the `fusion-runner` Python `KeycloakAuth` helper) can read credential keys as env vars. Overridable per-trigger (`WeaveTriggerSpec.authSecretRefOverride`) and per-run (`WeaveRunSpec.authSecretRefOverride`); precedence is run > trigger > chain.
- `WeaveTrigger` type `Kafka` — generic Kafka consumer trigger; fires one WeaveRun per message after applying configurable `eventFilter` (put/delete/get) and `bucketFilter` (bucket name list). Designed for S3/MinIO change events forwarded to Redpanda via `mc event add`.
- `WeaveTriggerSpec.kafka` — config struct with `brokers`, `topic`, `consumerGroup`, optional `secretRef` (SASL: keys `username`/`password`/`mechanism`), `eventFilter`, `bucketFilter`, `maxConcurrentRuns` (throttle cap; 0 = unlimited). Paused via `spec.paused=true` (same field as BatchCron).
- `internal/trigger.KafkaConsumer` — one goroutine per Kafka trigger using `segmentio/kafka-go`; filters applied before the fire channel; offsets committed on every message including filtered and throttled ones (skip policy).
- `internal/trigger.parseS3EventEnvVars` — MinIO S3 event JSON → `S3_EVENT_NAME`, `S3_BUCKET`, `S3_KEY`, `S3_SIZE`, `S3_ETAG`, `S3_EVENT_TIME`, `S3_EVENT_JSON` env vars injected into every triggered WeaveRun.
- `GET/POST/PUT/PATCH/DELETE /api/v1/kafkatriggers` — REST CRUD for Kafka triggers; body `{name, chainRef, kafka: {brokers, topic, consumerGroup, ...}}`.
- Local dev infrastructure: single-node Redpanda (Kafka) + MinIO (S3) Helm values in `deployment/local-dev/`; pre-configured so MinIO forwards all bucket events (put/delete/get) to Redpanda topic `s3-events`. Setup documented in `INSTALL.md` under "Local Development Dependencies".

- `WeaveTrigger` type `BatchCron` — a new trigger type that reads a list of jobs from a ConfigMap (key `jobs.yaml`) and schedules each job independently using its own cron expression. Each fired job creates a WeaveRun with `JOB_ID`, `JOB_NAME`, `JOB_TOPIC`, `JOB_MAINTAINER`, `JOB_STARTDATE`, `JOB_STARTTIME`, `JOB_SCHEDULE`, and `JOB_METADATA` (full metadata JSON) injected as env vars. Designed for thousands of concurrent jobs across multiple batch triggers without interfering with the existing `Cron` trigger.
- `WeaveTriggerSpec.batchCron.jobsConfigMapRef` — references the ConfigMap containing the YAML job list for `BatchCron` triggers.
- `WeaveTriggerSpec.paused` — suspends scheduling for any trigger type when `true`. Set via `POST /api/v1/batchtriggers/{name}/stop`.
- `WeaveTriggerStatus.batchJobCount` and `.batchJobErrors` — live count of valid/invalid jobs loaded from the ConfigMap.
- `internal/trigger.BatchCronScheduler` — isolated min-heap scheduler (O(log N) per fire, one goroutine per batch trigger). Separate from `CronScheduler`; no shared state.
- `internal/trigger.ParseBatchJobs` — YAML parser for the batch job list; returns valid `BatchJob` entries and per-line `ValidationError` entries.
- `POST /api/v1/batchtriggers` — create a batch trigger; body `{name, chainRef, jobs: "<yaml>"}`. Stores YAML in an auto-created ConfigMap owned by the trigger.
- `PUT /api/v1/batchtriggers/{name}` — replace the jobs YAML for an existing batch trigger.
- `POST /api/v1/batchtriggers/{name}/stop` — pause scheduling (`spec.paused=true`).
- `POST /api/v1/batchtriggers/{name}/resume` — upload new YAML and resume scheduling.
- `POST /api/v1/batchtriggers/validate` — validate jobs YAML; returns `{valid, errors: [{line, message}]}` without touching Kubernetes.
- ConfigMap RBAC added to `config/rbac/api-role.yaml` and `deployment/fusion-weave/templates/api-role.yaml` for the API server.
- `codeSource.writablePaths` Helm value and `WRITABLE_PATHS` operator env var — configurable list of paths that receive a writable `emptyDir` volume in both the code-loader init container and the main service container for deploy steps with a `codeSource`. Defaults to `/tmp`, `/home/nonroot`, `/weave-work`. Required when `readOnlyRootFilesystem: true` so runners can extract archives, install dependencies, and write temp/cache files.
- `Dockerfile.loader` — standalone build for the code-loader init container; produces a 5 MB distroless image containing only `/loader`. Push to your registry and set it as the cluster-wide default via `codeSource.loaderImage` in Helm.
- `codeSource.loaderImage` Helm value and `LOADER_IMAGE` operator env var — cluster-wide default init container image for code-source deploy steps. Applied to any deploy step whose `WeaveServiceTemplate` does not set `codeSource.loaderImage` explicitly. Falls back to `fusion-code-loader:latest` when unset.
- `WeaveRunStatus.Message` is now populated: chain-not-found, template-not-found, and invalid-DAG failures write a human-readable summary directly on the run; terminal runs aggregate all failed step messages into the run-level message so a single `kubectl get fr <name> -o jsonpath='{.status.message}'` shows the root cause.
- `WeaveRunStepStatus.Message` now captures the actual job failure reason: Job condition message, container exit code, or — for init-container failures such as a bad `INDEX_URL` — the last 10 lines of the code-loader log, so loader errors (artifact not found, index unreachable) are visible in the step status without requiring `kubectl logs`.
- `codeSource.indexURL` Helm value and `FUSION_INDEX_URL` operator env var — cluster-wide default base URL for fusion-index, applied to any deploy step whose `WeaveServiceTemplate` does not set `codeSource.indexURL` explicitly. Falls back to the built-in in-cluster default when unset.
- `WeaveRun.spec.stepOverrides` — per-step deployment parameters for deploy-kind steps. When a step is listed in `stepOverrides`, the operator creates a run-owned Deployment named `<runName>-<stepName>` instead of the chain-owned `<chainName>-<stepName>`, enabling a single shared `WeaveChain` to serve many service instances with different artifact, tag, and ingress host.
- `WeaveRun.status.activeDeployments` — tracks run-owned Deployments (created via `stepOverrides`) for code-source polling. Health monitoring and rolling restarts on artifact tag changes are handled by the run controller for these entries.
- `indexclient.FetchAppMetadata` — resolves an artifact tag in fusion-index and downloads + parses the artifact's `metadata.yaml`, returning runner type/port/args, resource requests/limits, and ingress path prefix. Used by the run controller to auto-configure run-owned Deployments without repeating those fields in every `WeaveServiceTemplate`.
- `deploybuilder.BuildFromOverride`, `BuildServiceFromOverride`, `BuildIngressFromOverride` — build run-owned Deployment, Service, and Ingress from a `WeaveRunStepOverride` + `AppMetadata` overlay on top of a base `WeaveServiceTemplate`.
- `deploybuilder.RunDeploymentName`, `RunServiceName`, `RunIngressName` — name helpers for run-owned resources (`<runName>-<stepName>`).
- `WeaveRunReconciler.CodeSourcePollInterval` — wired from `CODE_SOURCE_POLL_INTERVAL` env var (same as the chain reconciler); governs how often run-owned deployments are polled for artifact tag changes.

### Changed
- **Breaking:** Ingress hostnames are no longer free text. `WeaveIngressRule.host` is replaced by `WeaveIngressRule.name` (a DNS label only), and `WeaveRunStepOverride.ingressHost` is replaced by `WeaveRunStepOverride.ingressName`. The operator builds the actual Ingress host by appending a cluster-wide suffix — `ingress.hostSuffix` Helm value / `INGRESS_HOST_SUFFIX` env var — so a template or run can never point an Ingress at an arbitrary external hostname and accidentally hijack DNS. A `WeaveServiceTemplate` with `spec.ingress` set is invalid (`status.valid=false`) until `ingress.hostSuffix` is configured; the same guard applies to the `WeaveRunStepOverride.ingressName` path (fails the step, not the whole run).
- `cmd/loader/main.go` — loader no longer extracts archives; all downloaded files are now written as-is to `mountPath` using their original filename. The container image is responsible for any unpacking.
- `internal/deploybuilder` — every deploy-kind container now receives a consistent set of `WEAVE_*` env vars: `WEAVE_ARTIFACT`, `WEAVE_TAG`, `WEAVE_VERSION`, `WEAVE_NAMESPACE`, `WEAVE_MOUNT_PATH`, and — when `metadata.yaml` is present — `WEAVE_PORT`, `WEAVE_RUNNER_TYPE`, `WEAVE_BUILDER_IMAGE`, `WEAVE_MAINTAINER`, `WEAVE_INGRESS_PATH_PREFIX`, plus all `runner.args` keys. Previously `runner.args` injection only applied to the `stepOverrides` path; chain-owned deployments now receive them too. `WEAVE_VERSION` is kept accurate on rolling restarts triggered by tag changes.
- `internal/indexclient` — `AppMetadata` gains `Maintainer` (top-level) and `Runner.BuilderImage` fields, parsed from `metadata.yaml`. New `FetchAppMetadataAndVersion` returns metadata and resolved semver in one round-trip.

### Fixed
- `cmd/loader/main.go` — loader previously fetched only the first file for an artifact version. It now downloads **all** files: archives (`.tar.gz`, `.tgz`, `.zip`) are unpacked into `mountPath`; plain files (`.py`, `.yaml`) are written directly. The `.version` file is always written from the index-resolved semver, never from `metadata.yaml` content.
- `WeaveRunReconciler` — `r.Update` when adding/removing the `deploy-cleanup` finalizer replaced the entire spec, silently pruning `spec.stepOverrides` because the informer cache returns the unregistered field as nil in older operator builds. Changed both finalizer mutations to `r.Patch(ctx, &run, client.MergeFrom(...))` so only the metadata diff is sent and the spec is never touched.
- Helm chart `api-*.yaml` templates and `deployment.yaml` — `--reuse-values` upgrades no longer panic with nil pointer errors when `api`, `codeSource`, or `workload` were absent from user-supplied values; `WORKLOAD_SECURITY_DEFAULTS` falls back to chart-defined security defaults instead of rendering `{}`.
- `WeaveRunReconciler` — fatal user-input errors (missing chain/template, failed job/deploy-step creation, input data preparation failure) no longer silently requeue as Pattern A Go errors; they now fail the affected step or run with a descriptive `message` and structured log entry so the root cause is visible in both `kubectl get fr` and operator logs.
- `WeaveTriggerReconciler.syncBatchCronSource` — the status patch that records `batchJobCount`/`batchJobErrors` never set `status.active` (a required CRD field), so it was rejected by API server validation (`status.active: Required value`) on every reconcile of a brand-new `BatchCron` trigger, permanently blocking it from ever scheduling a job. Found via end-to-end testing on minikube. Fix: set `ft.Status.Active = true` in the same patch.

### Added
- Structured HTTP access logging via `log/slog` with per-request `request_id` correlation; every request emits one INFO line with method, path, client IP, status, and latency.
- Auth decision logging in the Auth middleware: DEBUG on success with principal, auth_method (apikey/oidc/sa/unauthenticated), and role; WARN on rejected requests; ERROR on internal auth failures.
- Resource context fields (`kind`, `name`) on all Kubernetes operation error logs in API handlers, making 500 errors directly queryable by resource.
- `LOG_LEVEL` env var (debug|info|warn|error, default info) and `LOG_FORMAT` env var (json|text, default json) for the API server HTTP layer, wired through Helm as `api.log.level` and `api.log.format`.
- Operator controller phase-transition logs now include `run` and `phase` fields for structured querying; trigger reconciliation logs include `trigger` and `chain` fields.
- `podSecurityContext` and `containerSecurityContext` fields on `WeaveJobTemplateSpec` and `WeaveServiceTemplateSpec`. When set, these override the operator-wide `WORKLOAD_SECURITY_DEFAULTS` for pods/containers created from that template, allowing per-workload user configuration (e.g. `runAsUser: 101` for nginx). The init container on deploy steps with `codeSource` also inherits the template's `containerSecurityContext`.

### Fixed
- Deploy-step Deployments, Services, and Ingresses are now deleted when a WeaveRun is killed or stopped. A `weave.fusion-platform.io/deploy-cleanup` finalizer is added to any run whose chain contains deploy-kind steps; the finalizer ensures teardown runs before the run object is garbage-collected, preventing zombie pods. Succeeded runs are exempt — their Deployments survive for rolling updates by future runs on the same chain.

### Added
- Helm-configurable security contexts, annotations, and labels for all pods managed by the operator.
  - `podSecurityContext` / `containerSecurityContext` / `podAnnotations` / `podLabels` values for the operator pod.
  - `api.podSecurityContext` / `api.containerSecurityContext` / `api.podAnnotations` / `api.podLabels` values for the API server pod (independently configurable).
  - `workload.security.podSecurityContext` / `workload.security.containerSecurityContext` / `workload.security.podAnnotations` / `workload.security.podLabels` values applied uniformly to every pod the operator creates (batch Job pods and deploy-step Deployment pods, including `code-loader` init containers).
  - `workload.security.podSecurityContext.seccompProfile` supported: set `type: RuntimeDefault` or `type: Localhost` with `localhostProfile: <path>`.
  - Previously hardcoded security contexts on operator and API pods are now values-driven defaults that can be overridden per environment.
- New `internal/security` package: `security.Defaults` struct used to carry workload security config from `cmd/main.go` through `WeaveRunReconciler` into `jobbuilder` and `deploybuilder`.

- `codeSource` field on `WeaveServiceTemplate`: declares a fusion-index artifact and tag; the operator injects a `code-loader` init container that resolves the tag, downloads the archive, and unpacks it to a configurable mount path before the main container starts.
- `fusion-platform.io/reload-deploy-step: <stepName>@<version>` annotation on `WeaveChain`: extensible one-shot trigger consumed by the chain reconciler; any external source (webhook, REST API, CI/CD) can set this annotation to cause a rolling restart that loads the new code version.
- WeaveChain controller polls fusion-index every `CODE_SOURCE_POLL_INTERVAL` (default 60 s) for tag changes on deploy steps with `codeSource`; rolling restart is triggered automatically when the resolved version changes.
- New `cmd/loader` binary (built into the operator image as `/loader`): init container entry point that resolves an artifact tag, downloads the archive from fusion-index, unpacks `.tar.gz` / `.zip` archives, and writes a `.version` file.
- New `internal/indexclient` package: minimal `net/http` client for fusion-index tag resolution, used by the operator's polling loop and at deployment registration time.
- Helm chart: `codeSource.pollInterval` value (default `"60s"`) maps to the `CODE_SOURCE_POLL_INTERVAL` env var on the operator pod.

---

## [0.2.0] — 2026-05-11

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
