# fusion-weave

## Changelog
Every bugfix and new feature must be recorded in `CHANGELOG.md` (Keep a Changelog format).
- Add entries under `## [Unreleased]` as you work; bump a version heading when releasing.
- Sections: `### Added`, `### Changed`, `### Fixed`, `### Removed`.
- One line per change — be specific enough that a reader understands scope without reading the diff.

## Project
Kubernetes operator in Go that schedules job DAGs. 5 CRDs: WeaveJobTemplate, WeaveServiceTemplate, WeaveChain, WeaveTrigger, WeaveRun.

## Architecture
- `api/v1alpha1/` — CRD type definitions (WeaveJobTemplate, WeaveServiceTemplate, WeaveChain, WeaveTrigger, WeaveRun)
- `internal/controller/` — 5 reconcilers, one per CRD; weaverun_controller.go is the main execution engine
- `internal/dag/` — pure-Go DAG engine (graph.go + executor.go); no Kubernetes dependency, fully unit-tested
- `internal/jobbuilder/` — translates WeaveJobTemplate + WeaveChainStep + WeaveRun into batch/v1 Job specs
- `internal/deploybuilder/` — builds apps/v1 Deployment + Service + Ingress for deploy-kind steps
- `internal/trigger/` — cron scheduler and webhook HTTP server
- `internal/apiserver/` — REST API service (chi router, auth middleware, CRUD handlers for all 5 CRDs)
- `internal/indexclient/` — minimal HTTP client for fusion-index tag resolution (used by code-source polling in chain controller)
- `cmd/main.go` — wires manager, registers controllers, creates shared fire channel
- `cmd/api/main.go` — entry point for the REST API server (separate from the operator)
- `cmd/loader/main.go` — init container entry point for codeSource deploy steps; built into operator image as `/loader`

## Logging (API server)
- Platform logging principles: `../logging_principles.md` (written for Gin, adapted here for chi).
- HTTP layer uses `log/slog`; operator controllers keep `logr/zap` from controller-runtime — both coexist in the same binary.
- Call-site interface in handlers: `middleware.LoggerFromCtx(r.Context())` — carries `request_id` and request context. Use `slog.Default()` for background/startup code outside a request.
- `LOG_LEVEL` / `LOG_FORMAT` env vars control the slog handler; wired via `api.log.level` / `api.log.format` in Helm.
- `internal/monitoring/handlers` intentionally imports `internal/apiserver/middleware` for `LoggerFromCtx` — no import cycle.

## Build
- `go build ./...` — standard build
- `make generate` — regenerate deepcopy + CRD YAML after changing api/v1alpha1/ types (requires `~/go/bin/controller-gen`)
- `cp config/crd/bases/*.yaml deployment/fusion-weave/crds/` — copy updated CRDs into Helm chart after `make generate`
- `kubectl apply -f config/crd/bases/` — install/update CRDs directly on the cluster (faster than Helm during type iteration)
- `make docker-build` — builds image inside minikube (`eval $(minikube docker-env)` is handled by the Makefile)
- `make minikube-deploy` — raw-YAML deploy (config/ manifests, no Helm); use for quick iteration without chart overhead
- `make test` — runs `go test ./... -v`; covers dag, indexclient, and deploybuilder packages

## Testing
- Package naming: tests of unexported functions use `package <pkg>` (same package); exported-function tests use `package <pkg>_test` — match whatever the existing test file in that package uses.
- HTTP layer tests (indexclient) use `net/http/httptest` + Go 1.22+ `http.NewServeMux()` with method-qualified routes (`"GET /api/v1/..."`) and path wildcards (`{id}`).
- `sigs.k8s.io/yaml` v1.4.0: `port: "8080"` (quoted YAML string) causes a parse error — YAML string → JSON string → `encoding/json` cannot unmarshal into `int32`. Unknown fields are silently ignored.

## Key gotchas
- controller-runtime v0.19.x required (not v0.18.x) — `MetricsBindAddress` renamed to `Metrics: metricsserver.Options{BindAddress: ...}`.
- `r.Get()` zeroes out TypeMeta — set it explicitly before `metav1.NewControllerRef()` or owner refs get blank apiVersion and GC won't work.
- `client.MergeFrom(run.DeepCopy())` must be captured immediately after `r.Get()`, before any mutations, or status diffs will be empty.
- Cron/webhook callbacks run outside the reconcile loop — use a `source.Channel` + `WatchesRawSource` to wake the reconciler, not just `storePendingFire`.
- Map iteration in Go is non-deterministic — sort step names before writing `run.Status.Steps` to avoid spurious status diffs.
- Both `config/rbac/role.yaml` AND `deployment/fusion-weave/templates/role.yaml` are hand-maintained — `make generate` does NOT update either; edit both manually when CRD resource names or API group change.
- Terminal runs (Succeeded/Failed/Stopped) that still hold the `weave.fusion-platform.io/deploy-cleanup` finalizer are re-reconciled once to run cleanup and remove the finalizer — then no further reconciliation. To test a controller fix, always fire a new run.
- `dag.Advance` is called **twice** in `weaverun_controller.go`: once before the decisions loop (to get decisions) and once after (to recompute `RunComplete` using the post-decision `stepStates`). Do NOT remove the second call — without it, deploy steps cause the WeaveRun to complete immediately on first reconcile because the pre-decision states map is empty.
- `deploybuilder.Build` (chain-owned) vs `BuildFromOverride` (run-owned) port asymmetry: `Build` writes `WEAVE_PORT` env var from `meta.Runner.Port` but never replaces container/service ports — those always come from `tmpl.Spec.Ports`. `BuildFromOverride` replaces ports entirely when `meta.Runner.Port > 0`. Do not assume they behave the same.
- `runner.args` keys that collide with `tmpl.Spec.Env` entries produce silent duplicate env vars — the builder appends without deduplication. Template env comes first; runner.args appended after (runtime last-value-wins means runner.args effectively overrides).
- Removing a `+kubebuilder:default` annotation from a type does NOT clear the value from existing CRs — K8s wrote the default into each object at creation time. Production CRs silently ignore the operator env-var fallback (e.g. `FUSION_INDEX_URL`) because the field is non-empty. Fix: `kubectl patch <resource> <name> -n fusion --type=merge -p '{"spec":{"fieldName":""}}'` on each affected object.

## Deploy / test cycle on minikube
```
eval $(minikube docker-env) && docker build -t fusion-weave-operator:latest .
kubectl rollout restart deployment/fusion-weave-operator -n fusion
kubectl rollout restart deployment/fusion-weave-api -n fusion
kubectl annotate weavetrigger <name> fusion-platform.io/fire=true --overwrite -n fusion   # on-demand fire; --overwrite required if trigger was already fired
kubectl annotate weaveruns <run-name> fusion-platform.io/restart-step=<stepName> --overwrite -n fusion   # rolling restart a Deployed-phase deploy step; one-shot, annotation consumed by reconciler
kubectl annotate weavechains <chain-name> fusion-platform.io/reload-deploy-step=<stepName>@<version> --overwrite -n fusion   # trigger code-source rolling restart; --overwrite required
kubectl get fr -n fusion -w    # watch runs  (shortNames: fr=WeaveRun, ft=WeaveTrigger, fc=WeaveChain, fjt=WeaveJobTemplate, wst=WeaveServiceTemplate)
kubectl get fr <name> -n fusion -o jsonpath='{.status.phase} {range .status.steps[*]}{.name}={.phase} {end}'   # inspect run+step phases in one shot
kubectl get jobs -n fusion     # watch batch jobs
kubectl port-forward svc/fusion-weave-api 8082:8082 -n fusion &   # expose REST API locally
kubectl port-forward svc/fusion-index-backend 8099:8080 -n fusion &   # expose fusion-index locally for tag-move tests
# WeaveRun API group is weave.fusion-platform.io (NOT fusion-platform.io) — use correct group when kubectl-applying runs manually:
# apiVersion: weave.fusion-platform.io/v1alpha1
```

## Namespace
Operator is scoped to namespace `fusion`. Cache is constrained via `cache.Options{DefaultNamespaces: ...}`.
RBAC is a namespaced Role (not ClusterRole) — do not expand scope without updating both.

## Step output / input passing
- Steps opt in with `producesOutput: true` (WeaveChainStep) — operator captures JSON stdout after job succeeds.
- Consumers declare `consumesOutputFrom: [stepA, stepB]` — operator writes merged JSON to `/weave-input/input.json` inside the container.
- Merged JSON is namespaced by producer: `{"stepA": {...}, "stepB": {...}}` — no flat-merge to avoid key collisions.
- Per-run ConfigMap `<runName>-outputs`: keys `step-<name>` (captured output), `input-<name>` (merged input for consumer).
- Chain controller validates at admission: every `consumesOutputFrom` entry must reference an ancestor step with `producesOutput: true`.
- `prepareInputData` returns `(cmName, ready bool, err)` — `ready=false` means requeue (producer not yet captured), not an error.
- `WeaveRunStepStatus.OutputCaptured` guards the capture-and-write path — checked before calling `captureStepOutput` to prevent double-capture on requeue.

## Shared storage (per-run PVC)
- Opt-in per chain: `spec.sharedStorage: {size: "500Mi", storageClassName: "csi-hostpath-sc"}` — mounts `/weave-shared` into every job pod (ReadWriteMany).
- PVC named `<runName>-shared`, owned by WeaveRun — GC'd automatically on run deletion.
- minikube default StorageClass (`standard`) does NOT support RWX — enable the addon first: `minikube addons enable csi-hostpath-driver` (StorageClass: `csi-hostpath-sc`).
- Chain controller validates `sharedStorage.size` is a parseable resource quantity at admission time.

## Deploy steps (WeaveServiceTemplate)
- `stepKind: Deploy` on a WeaveChainStep creates/rolling-updates an `apps/v1 Deployment` + Service + optional Ingress.
- Stable resource name: `<chainName>-<stepName>` — same across runs, enabling rolling updates.
- Owner = WeaveChain (not WeaveRun): resources survive run deletion. Never patch `spec.selector` after creation.
- Selector labels `fusion-platform.io/chain` + `fusion-platform.io/step` — immutable, never include run name.
- Step transitions to `StepPhaseDeployed` (non-terminal) when `Deployment.Available=True` — **not** `Succeeded`.
- `Deployed` satisfies dependency checks (downstream steps can start once the service is up) but blocks `RunComplete` — the WeaveRun stays `Running` for the lifetime of the service.
- Deploy step lifecycle: `Pending → Running → Deployed`; transitions to `Failed` only if the Deployment is deleted externally.
- `RunComplete` is NEVER true while any step is `StepPhaseDeployed` (executor.go:111-119) — StopAll policy cannot kill a Deployed step; only explicit run deletion or a direct status patch to Stopped/Failed can.
- `weave.fusion-platform.io/deploy-cleanup` finalizer is added to any WeaveRun whose chain has deploy-kind steps; on deletion or entering Stopped/Failed it deletes the Deployment + Service + Ingress and removes the `ActiveDeployments` entry before allowing GC. `doDeployTeardown` is the only code path that removes `ActiveDeployments` entries.
- Succeeded runs are exempt from teardown — Deployments survive so future runs on the same chain can rolling-update them.
- WeaveChain controller monitors health independently and auto-rollbacks after `unhealthyDuration`.
- After adding ANY field to a spec/status struct (not just enums): run `make generate` + `kubectl apply -f config/crd/bases/` — K8s silently prunes unknown fields until CRDs are updated; `kubectl set image` bypasses Helm and never updates CRDs, so this step is always manual after a `set image` deploy.
- **`kubectl apply` SSA timing**: even after `kubectl apply -f config/crd/bases/` completes, the API server may silently prune new spec fields on object creation for a short window. Use `kubectl create` (not `kubectl apply`) when testing a resource with newly-added fields; `kubectl create` errors on unknown fields rather than silently dropping them.
- `r.Update(ctx, &run)` sends the **entire** object to the API server, replacing the spec with whatever the informer cache returned — if the cache was built by an older operator binary (before a type added a field), the new field is nil and will be erased. Use `r.Patch(ctx, &run, client.MergeFrom(run.DeepCopy()))` for metadata-only changes (e.g. finalizers) to avoid touching the spec.
- When tagging a new operator image for minikube: the Helm/raw-YAML deployment uses a fixed version tag (e.g. `0.2.1`); `make docker-build` or `docker build -t fusion-weave-operator:latest` only creates the `latest` tag. Always re-tag: `docker tag fusion-weave-operator:latest fusion-weave-operator:0.2.1` after building so the deployment picks up the new binary.
- `podSecurityContext` / `containerSecurityContext` on `WeaveJobTemplateSpec` and `WeaveServiceTemplateSpec` override the global `WORKLOAD_SECURITY_DEFAULTS` for that template's pods. Builder pattern: compute `podSC`/`containerSC` variables at the top of the builder function (`if tmpl.Spec.X != nil { use tmpl } else { use sec }`), then reference the variable in the struct literal. `containerSecurityContext` applies to both the main container and the `code-loader` init container on deploy steps.
- `codeSource.loaderImage` in minikube: default `fusion-code-loader:latest` does not exist — always set `loaderImage: fusion-weave-operator:<version>` (the operator image embeds `/loader`). See `config/samples/weavechain_test_template.yaml`.
- `spec.codeSource` on WeaveServiceTemplate injects a `code-loader` init container: resolves a fusion-index artifact tag → version, downloads each file, **copies it as-is** to `codeSource.mountPath` (default `/weave-code`) — no extraction, the runner image handles that. Writes `.version` with the resolved semver. Fires on every pod start, including first start of a new run. Tracks state in `WeaveChain.Status.ActiveDeployments[*].CodeSource*` fields.
- `fusion-platform.io/reload-deploy-step: <stepName>@<version>` annotation on WeaveChain — one-shot rolling-restart trigger for code-source steps; consumed by `handleCodeReload` in chain controller. External callers (CI/CD, REST API, webhook) set this annotation; the internal polling loop calls `triggerCodeReload` directly without going through the annotation.
- `CODE_SOURCE_POLL_INTERVAL` operator env var — how often the chain controller polls fusion-index for tag changes (default `60s`); set in `cmd/main.go`, stored on `WeaveChainReconciler.CodeSourcePollInterval`.
- Health loop in `syncDeploymentHealth` uses `if/else` (not `continue`) so code-source polling runs for every entry regardless of deployment health — do not revert to `continue`-based structure.
- fusion-index tag move for polling tests: `PUT /api/v1/artifacts/{id}/tags/{tag}` requires **both** `"version":"1.0.1"` and `"versionId":1001` — `versionId` alone returns 400. Same for `POST .../versions`: requires `"version":"1.0.1"` in addition to major/minor/patch.
- fusion-index-backend has no PVC — artifact file bytes are lost on pod restart even though DB records survive. If downloads return 500, re-upload via `POST /api/v1/artifacts/{id}/versions/{version}/files` (multipart `file=@...`). Use `DELETE /api/v1/artifacts/{id}/versions/{version}` first if stale records conflict.
- `indexclient.FetchAppMetadataAndVersion` (preferred over `FetchAppMetadata` + `ResolveTag`) is called for both chain-owned and override deployments to populate WEAVE_* env vars. `metadata.yaml` fields: `runner.type`, `runner.port` (int32), `runner.args` (map style — NOT list of `{name,value}`), `runner.builderImage`, `ingress.pathPrefix`, `resources.requests/limits` (standard k8s quantities), `maintainer` (top-level string).
- `WRITABLE_PATHS` operator env var (`codeSource.writablePaths` in Helm, `:`-separated) — mounts a writable `emptyDir` at each path in both the `code-loader` init container and the main service container, **only when `codeSource` is configured** on the step. Required when `readOnlyRootFilesystem: true` so runners can write to `/tmp`, install deps, etc. Default paths: `/tmp`, `/home/nonroot`, `/weave-work`. `config/manager/manager.yaml` sets these defaults for raw-YAML deploys; Helm reads them from `values.yaml`.
- `deploybuilder.writableVolumeName` sanitizes paths to valid K8s volume names (DNS label: lowercase `[a-z0-9-]`, max 63 chars) and deduplicates against existing volumes — do not add raw path-derived names elsewhere without the same sanitization.
- Every deploy-kind container with `codeSource` receives `WEAVE_ARTIFACT`, `WEAVE_TAG`, `WEAVE_VERSION`, `WEAVE_NAMESPACE`, `WEAVE_MOUNT_PATH` automatically, plus `WEAVE_PORT`, `WEAVE_RUNNER_TYPE`, `WEAVE_BUILDER_IMAGE`, `WEAVE_MAINTAINER`, `WEAVE_INGRESS_PATH_PREFIX`, and all `runner.args` keys when metadata is available. `deploybuilder.UpdateVersionEnvVar(deploy.Spec.Template.Spec.Containers, newVersion)` must be called alongside the `restartedAt` annotation patch in any rolling-restart trigger (`triggerCodeReload`, `pollRunDeploymentCodeSource`) to keep `WEAVE_VERSION` accurate in new pods.

## REST API (cmd/api)
- `cmd/api/` binary is separate from the operator; build with `go build ./cmd/api` or run with `go run ./cmd/api`.
- Auth modes: API key (Secret with label `fusion-platform.io/api-key=true`, role from annotation `fusion-platform.io/role`), OIDC JWT, SA TokenReview (role from SA label `fusion-platform.io/role`).
- Roles: `viewer`=GET, `editor`=GET/POST/PUT/PATCH, `admin`=all including DELETE.
- **chi routing gotcha**: health routes (`/healthz`, `/readyz`) must be registered on the root router *before* attaching `Auth`/`RBAC` middleware — registering them in a sub-group with `SkipAuth` does not work because global middleware runs first.
- Lazy auth init uses `sync.Once` — OIDC JWKS discovery happens on first request, not at startup.
- `AllowUnauthenticated=true` grants full admin to all callers — logs a warning; never use in production.
- SA auth requires a ClusterRole for TokenReview (cluster-scoped resource) — gated on `api.auth.saAuthEnabled` in Helm; raw YAML in `config/rbac/api-clusterrole.yaml`.
- Three binaries (`/manager`, `/api-server`, `/loader`) are built into the same Docker image; API deployment overrides entrypoint with `command: ["/api-server"]`; codeSource init containers use `command: ["/loader"]`.
- Raw-YAML manifest for quick iteration: `kubectl apply -f config/rbac/api-*.yaml -f config/manager/api-server.yaml`
- `PATCH /api/v1/{resource}/{name}` sends a JSON Merge Patch directly to Kubernetes — callers can set any metadata field including annotations (e.g. `{"metadata":{"annotations":{"fusion-platform.io/restart-step":"stepName"}}}`).

## Helm chart (deployment/fusion-weave/)
- Install on minikube: `helm upgrade --install fusion-weave deployment/fusion-weave/ --set image.repository=fusion-weave-operator --set image.tag=latest --set image.pullPolicy=Never --set namespace=fusion --set namespaceCreate=false`
- `namespaceCreate=false` when namespace pre-exists (avoids Helm ownership conflict on re-install)
- CRDs live in `crds/` — Helm installs them first, never deletes on uninstall; update manually with `kubectl apply -f deployment/fusion-weave/crds/` after `make generate`
- Deploy with samples (shared-storage demo chain): add `--set samples.enabled=true --set samples.sharedStorage.storageClassName=csi-hostpath-sc`
- `codeSource.pollInterval` — Go duration string controlling how often the chain controller polls fusion-index for tag changes (default `"60s"`); sets `CODE_SOURCE_POLL_INTERVAL` on the operator pod.
- `api.enabled=false` to skip deploying the API server entirely.
- `api.auth.saAuthEnabled=true` to enable SA TokenReview auth (also creates ClusterRole + ClusterRoleBinding for tokenreviews).

## Monitoring API (internal/monitoring/)
- Routes mounted at `/monitor/v1/` — enabled with `MONITORING_ENABLED=true`; disabled by default.
- Prometheus metrics served on a **separate port** (`METRICS_ADDR=:9091`, `/metrics`); no auth middleware on that port.
- Package structure: `internal/monitoring/cache/` (TTLCache), `internal/monitoring/logsink/` (Sink interface + KafkaSink/NoopSink), `internal/monitoring/handlers/` (Base + per-resource handlers), `internal/monitoring/routes.go`, `internal/monitoring/metrics_server.go`.
- `handlers.Base` carries all shared deps; `cacheGet(w, key) bool` is the DRY cache-hit helper used by every handler method.
- **KafkaSink shutdown pattern**: uses `stop chan struct{}` (closed by `Close()`) and `done chan struct{}` (closed by drainLoop). `Publish` selects on stop to avoid panic; `Close()` waits on `<-done` for full flush. Do NOT close the send channel directly.
- **Pod log fetching**: must use `kubeClient.CoreV1().Pods().GetLogs()` (typed client), not `client.Client`; container always named `"job"` per jobbuilder convention.
- **CRD field selectors on spec.* fields don't work server-side** — `ChainStats` lists all runs and filters in-process.
- `inWindow()` for stats: active runs (no CompletionTime) included only if `StartTime.After(cutoff)` — prevents old stuck runs from inflating stats.
- `?fieldSelector=` validated with `fieldSelectorRe` before use and before building the cache key (prevents cache-key injection).
- RBAC: `config/rbac/api-role.yaml` AND `deployment/fusion-weave/templates/api-role.yaml` both updated with monitoring rules (batch/jobs, apps/deployments, events, pods, pods/log).
- Helm: `api.monitoring.enabled=true`, `api.monitoring.metricsPort=9091`, `api.monitoring.cacheTTL`, `api.monitoring.maxLogLines`, `api.monitoring.kafka.brokers/topic`.
- **Raw-YAML deploy**: enabling monitoring requires `kubectl apply -f config/rbac/api-role.yaml` explicitly — patching the Deployment env vars alone does NOT update the Role; missing rules cause 500s on `/monitor/v1/runs/{name}`.
- **Port 9091 on raw-YAML**: the Service does not expose the metrics port unless installed via Helm with `api.monitoring.enabled=true`; use `kubectl port-forward pod/<api-pod-name> 8082:8082 9091:9091 -n fusion` (pod, not svc) when testing locally.
- **zsh curl gotcha**: always single-quote URLs containing `?` — zsh expands `?` as a glob (`curl -s 'http://localhost:8082/monitor/v1/stats/runs?window=7d'`).
- **Prometheus labeled metrics invisible until first use** — `GaugeVec`/`HistogramVec` via `promauto` emit no `/metrics` lines until a label combination is set; after deploying new metrics, call `GET /monitor/v1/runs` to populate them.
- **`GET /monitor/v1/runs` is the single metrics refresh trigger** — on a cache miss it refreshes all Tier-1 run/step gauges, the `weave_run_duration_seconds` histogram, and Tier-2 chain gauges via `refreshChainMetrics`. No background scrape loop exists.
- **Histogram deduplication**: `RunsHandler.seenRuns` tracks which terminal runs have been observed in `weave_run_duration_seconds` to prevent double-counting across polling cycles. Any new histogram added to a poll-based handler must use the same pattern.
- **chi middleware `RoutePattern()` timing**: `chi.RouteContext(r.Context()).RoutePattern()` is only populated *after* `next.ServeHTTP()` returns — read it after the call in `MonitoringMiddleware`.
