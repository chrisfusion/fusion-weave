# fusion-weave

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
- `cmd/main.go` — wires manager, registers controllers, creates shared fire channel
- `cmd/api/main.go` — entry point for the REST API server (separate from the operator)

## Build
- `go build ./...` — standard build
- `make generate` — regenerate deepcopy + CRD YAML after changing api/v1alpha1/ types (requires `~/go/bin/controller-gen`)
- `cp config/crd/bases/*.yaml deployment/fusion-weave/crds/` — copy updated CRDs into Helm chart after `make generate`
- `kubectl apply -f config/crd/bases/` — install/update CRDs directly on the cluster (faster than Helm during type iteration)
- `make docker-build` — builds image inside minikube (`eval $(minikube docker-env)` is handled by the Makefile)
- `make minikube-deploy` — raw-YAML deploy (config/ manifests, no Helm); use for quick iteration without chart overhead
- `make test` — runs unit tests (DAG engine is pure Go, no envtest needed)

## Key gotchas
- controller-runtime v0.19.x required (not v0.18.x) — `MetricsBindAddress` renamed to `Metrics: metricsserver.Options{BindAddress: ...}`.
- `r.Get()` zeroes out TypeMeta — set it explicitly before `metav1.NewControllerRef()` or owner refs get blank apiVersion and GC won't work.
- `client.MergeFrom(run.DeepCopy())` must be captured immediately after `r.Get()`, before any mutations, or status diffs will be empty.
- Cron/webhook callbacks run outside the reconcile loop — use a `source.Channel` + `WatchesRawSource` to wake the reconciler, not just `storePendingFire`.
- Map iteration in Go is non-deterministic — sort step names before writing `run.Status.Steps` to avoid spurious status diffs.
- Both `config/rbac/role.yaml` AND `deployment/fusion-weave/templates/role.yaml` are hand-maintained — `make generate` does NOT update either; edit both manually when CRD resource names or API group change.

## Deploy / test cycle on minikube
```
eval $(minikube docker-env) && docker build -t fusion-weave-operator:latest .
kubectl rollout restart deployment/fusion-weave-operator -n fusion
kubectl rollout restart deployment/fusion-weave-api -n fusion
kubectl annotate weavetrigger <name> fusion-platform.io/fire=true -n fusion   # on-demand fire
kubectl get fr -n fusion -w    # watch runs  (shortNames: fr=WeaveRun, ft=WeaveTrigger, fc=WeaveChain, fjt=WeaveJobTemplate, wst=WeaveServiceTemplate)
kubectl get jobs -n fusion     # watch batch jobs
kubectl port-forward svc/fusion-weave-api 8082:8082 -n fusion &   # expose REST API locally
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
- Step succeeds when `Deployment.Available=True`; WeaveChain controller then monitors health and auto-rollbacks after `unhealthyDuration`.

## REST API (cmd/api)
- `cmd/api/` binary is separate from the operator; build with `go build ./cmd/api` or run with `go run ./cmd/api`.
- Auth modes: API key (Secret with label `fusion-platform.io/api-key=true`, role from annotation `fusion-platform.io/role`), OIDC JWT, SA TokenReview (role from SA label `fusion-platform.io/role`).
- Roles: `viewer`=GET, `editor`=GET/POST/PUT/PATCH, `admin`=all including DELETE.
- **chi routing gotcha**: health routes (`/healthz`, `/readyz`) must be registered on the root router *before* attaching `Auth`/`RBAC` middleware — registering them in a sub-group with `SkipAuth` does not work because global middleware runs first.
- Lazy auth init uses `sync.Once` — OIDC JWKS discovery happens on first request, not at startup.
- `AllowUnauthenticated=true` grants full admin to all callers — logs a warning; never use in production.
- SA auth requires a ClusterRole for TokenReview (cluster-scoped resource) — gated on `api.auth.saAuthEnabled` in Helm; raw YAML in `config/rbac/api-clusterrole.yaml`.
- Both binaries (`/manager` and `/api-server`) are built into the same Docker image; API deployment overrides the entrypoint with `command: ["/api-server"]`.
- Raw-YAML manifest for quick iteration: `kubectl apply -f config/rbac/api-*.yaml -f config/manager/api-server.yaml`

## Helm chart (deployment/fusion-weave/)
- Install on minikube: `helm upgrade --install fusion-weave deployment/fusion-weave/ --set image.repository=fusion-weave-operator --set image.tag=latest --set image.pullPolicy=Never --set namespace=fusion --set namespaceCreate=false`
- `namespaceCreate=false` when namespace pre-exists (avoids Helm ownership conflict on re-install)
- CRDs live in `crds/` — Helm installs them first, never deletes on uninstall; update manually with `kubectl apply -f deployment/fusion-weave/crds/` after `make generate`
- Deploy with samples (shared-storage demo chain): add `--set samples.enabled=true --set samples.sharedStorage.storageClassName=csi-hostpath-sc`
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
