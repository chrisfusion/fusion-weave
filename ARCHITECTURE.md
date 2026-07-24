# Architecture

fusion-weave is a Kubernetes operator that schedules configurable job DAGs. This document describes how it is structured, how the components interact, and how a run moves from trigger to completion.

---

## Table of Contents

1. [Overview](#overview)
2. [CRD data model](#crd-data-model)
3. [Repository layout](#repository-layout)
4. [Operator internals](#operator-internals)
   - [Controller-runtime manager](#controller-runtime-manager)
   - [WeaveJobTemplate / WeaveServiceTemplate reconcilers](#weavejobtemplate--weaveservicetemplate-reconcilers)
   - [WeaveChain reconciler](#weavechain-reconciler)
   - [WeaveTrigger reconciler and the fire channel](#weavetrigger-reconciler-and-the-fire-channel)
   - [BatchCron and Kafka trigger internals](#batchcron-and-kafka-trigger-internals)
   - [WeaveRun reconciler — the execution engine](#weaverun-reconciler--the-execution-engine)
5. [DAG engine](#dag-engine)
6. [Job builder](#job-builder)
7. [Deploy builder](#deploy-builder)
8. [Code-source artifact loading](#code-source-artifact-loading)
9. [Run-owned deployments (WeaveRun.spec.stepOverrides)](#run-owned-deployments-weaverunspecstepoverrides)
10. [Step output passing](#step-output-passing)
11. [Shared storage](#shared-storage)
12. [Deploy steps and health monitoring](#deploy-steps-and-health-monitoring)
13. [Pod/container security defaults](#podcontainer-security-defaults)
14. [Backup and restore](#backup-and-restore)
15. [REST API server](#rest-api-server)
16. [Monitoring API server](#monitoring-api-server)
17. [End-to-end flow](#end-to-end-flow)

---

## Overview

The operator follows the standard Kubernetes controller pattern: it watches a set of custom resources and continuously reconciles the observed state of the cluster towards the desired state declared in those resources.

There are five Custom Resource Definitions (CRDs). Three are configuration (templates and a chain definition), one is an activation source (trigger), and one is a mutable execution record (run):

```
WeaveJobTemplate     ──┐
WeaveServiceTemplate ──┼──► WeaveChain ──► WeaveTrigger ──► WeaveRun(s)
                        └──────────────────────────────────►
```

When a trigger fires it creates a `WeaveRun`. The run reconciler drives the DAG forward one step at a time, creating Kubernetes `batch/v1 Jobs` (or `apps/v1 Deployments`) for each chain step and updating the run's status until every step reaches a terminal phase.

Beyond the core DAG engine, three cross-cutting subsystems have been added since the initial design:

- **Code-source artifact loading** — Job and Deploy steps can pull a versioned build artifact from `fusion-index` at pod start instead of baking it into the container image.
- **Run-owned deployments** (`WeaveRun.spec.stepOverrides`) — a deploy-kind step can be instantiated per-run (`<runName>-<stepName>`) instead of once per chain (`<chainName>-<stepName>`), so multiple concurrent runs of the same chain can each get their own live service instance.
- **CRD backup/restore** — a separate one-shot `/backup` binary dumps WeaveJobTemplate/WeaveServiceTemplate/WeaveChain/WeaveTrigger specs to S3 on a daily CronJob, for disaster recovery.

---

## CRD data model

### WeaveJobTemplate

A reusable pod spec for a single batch job step. Stores image, command, args, env, resource requests/limits, and retry policy (`api/v1alpha1/weavejobtemplate_types.go`). Referenced by WeaveChain steps.

- `podSecurityContext` / `containerSecurityContext` — optional per-template overrides of the operator-wide `WORKLOAD_SECURITY_DEFAULTS` (see [Pod/container security defaults](#podcontainer-security-defaults)). When set, they replace the operator default entirely rather than merging with it.
- `codeSource` (`*CodeSourceSpec`) — when set, `jobbuilder.Build` injects a `code-loader` init container that resolves `artifactName@tag` in fusion-index and copies the files into `mountPath` (default `/weave-code`) before the job container starts. Unlike Deploy-kind steps there is no polling/rolling-restart concern: every WeaveRun creates a fresh Job pod, so the tag is re-resolved on every run automatically.

### WeaveServiceTemplate

A reusable spec for a long-running deployment step: image, replicas, ports, probes, service type, optional Ingress rules, `unhealthyDuration` (auto-rollback threshold), and `revisionHistoryLimit` (`api/v1alpha1/weaveservicetemplate_types.go`). Referenced by chain steps with `stepKind: Deploy`.

- `podSecurityContext` / `containerSecurityContext` — same override semantics as WeaveJobTemplate; `containerSecurityContext` applies to both the main container and the `code-loader` init container.
- `codeSource` (`*CodeSourceSpec`) — see [Code-source artifact loading](#code-source-artifact-loading). For Deploy-kind steps this is the mechanism that keeps a long-running service's code up to date: the chain controller polls fusion-index and triggers a rolling restart when the tracked tag moves.

`CodeSourceSpec` fields (shared by both templates, `api/v1alpha1/weaveservicetemplate_types.go`):

| Field | Purpose |
|---|---|
| `indexURL` | fusion-index base URL; falls back to `FUSION_INDEX_URL` env var, then the in-cluster default |
| `artifactName` | full artifact name in fusion-index, e.g. `org.myteam.myapp` |
| `tag` | mutable tag to track, e.g. `stable` |
| `mountPath` | directory inside the main container where unpacked code appears (default `/weave-code`) |
| `loaderImage` | init container image; falls back to `LOADER_IMAGE` env var, then `fusion-code-loader:latest` |
| `loaderImagePullPolicy` | defaults to `IfNotPresent` |

### WeaveChain

The DAG definition. Contains an ordered list of steps, each with:
- `stepKind` — `Job` (default) or `Deploy`
- `jobTemplateRef` / `serviceTemplateRef` — which template to use
- `dependsOn` — list of step names that must complete first
- `runOnSuccess` / `runOnFailure` — conditional execution flags
- `envOverrides` — per-step environment variable overrides
- `producesOutput` / `consumesOutputFrom` — output-passing declarations
- `failurePolicy` — `StopAll`, `ContinueOthers`, or `RetryFailed`
- `sharedStorage` — optional RWX PVC provisioned once per run
- `authSecretRef` (`WeaveChainSpec.AuthSecretRef`, `*corev1.LocalObjectReference`) — optionally names a Secret injected via `envFrom` into every step pod of the chain (Job and Deploy kind alike). Its keys become environment variables that runner-side helper libraries (e.g. a Python Keycloak-auth helper) use to obtain access tokens. Overridable per-trigger (`WeaveTriggerSpec.AuthSecretRefOverride`) or per-run (`WeaveRunSpec.AuthSecretRefOverride`) — resolved once per reconcile by `WeaveRunReconciler.resolveAuthSecretName`.

`WeaveChainStatus.ActiveDeployments` maps stable chain-owned Deployment names (`<chainName>-<stepName>`) to a `WeaveActiveDeploymentStatus` entry, which now also carries `CodeSourceArtifact` / `CodeSourceTag` / `CodeSourceIndexURL` / `CodeSourceDeployedVersion` — the state the chain controller's polling loop needs to detect a tag move and trigger a rolling restart (see [Code-source artifact loading](#code-source-artifact-loading)).

### WeaveTrigger

An activation source for a WeaveChain. Five types (`WeaveTriggerType`, `api/v1alpha1/weavetrigger_types.go`):

- `Cron` — fires on a robfig/cron schedule (6-field, seconds-prefixed)
- `OnDemand` — fires when the annotation `fusion-platform.io/fire=true` is set
- `Webhook` — fires on an authenticated HTTP POST to a configured path
- `BatchCron` — fires many independently-scheduled jobs from a YAML job list stored in a ConfigMap (`spec.batchCron.jobsConfigMapRef`); each job has its own 5-field standard cron schedule and optional `startdate`/`starttime`
- `Kafka` — fires on messages consumed from a Kafka topic (`spec.kafka`), filtered by S3-event type (`put`/`delete`/`get`) and bucket name; optional SASL auth via a Secret reference

`WeaveTriggerSpec.AuthSecretRefOverride` overrides the chain's `authSecretRef` for every run this trigger creates; `WeaveTriggerStatus` gained `BatchJobCount` / `BatchJobErrors` (BatchCron only) alongside the pre-existing `Active`, `LastScheduleTime`, `LastRunName`, `WebhookURL`, `PendingRuns`.

### WeaveRun

The mutable execution record. Created by the trigger reconciler; driven by the run reconciler. Stores the overall phase (`Running`, `Succeeded`, `Failed`, `Stopped`), per-step status (phase, job ref, deployment ref, output captured, retry count), start/completion times, and the name of any shared PVC provisioned for this run.

- `spec.stepOverrides` (`[]WeaveRunStepOverride`) — see [Run-owned deployments](#run-owned-deployments-weaverunspecstepoverrides).
- `spec.authSecretRefOverride` — overrides both the chain default and any trigger override, for this run only.
- `status.activeDeployments` — mirrors `WeaveChainStatus.ActiveDeployments` but tracks **run-owned** Deployments created via `stepOverrides`; the run controller (not the chain controller) does health/code-source polling for these entries.

---

## Repository layout

```
cmd/
  main.go            — operator entry point: builds the manager, registers all 5 reconcilers
  api/
    main.go          — REST API server entry point (separate binary)
    config.go        — flag/env var configuration
  loader/
    main.go          — code-loader init container entry point, built into the operator
                        image as /loader; resolves an artifact tag and copies files
  backup/
    main.go           — /backup binary entry point (dump/restore subcommands)
    config.go          — env var configuration shared by both subcommands
    backup.go          — runBackup: stream CRD dump → gzip → S3 upload
    restore.go          — runRestore: S3 download → gunzip → restore, FORCE-gated

api/v1alpha1/        — CRD Go types + deepcopy generated code

internal/
  controller/        — 5 reconcilers
    weavejobtemplate_controller.go
    weaveservicetemplate_controller.go
    weavechain_controller.go     ← deploy health monitoring + code-source polling
    weavetrigger_controller.go   ← Cron/OnDemand/Webhook/BatchCron/Kafka
    weaverun_controller.go       ← main execution engine
    deploy_helpers.go            — isDeploymentAvailable()

  dag/
    graph.go         — DAG construction + topological sort (Kahn's algorithm)
    executor.go       — pure Advance() function: no k8s dependency

  jobbuilder/
    builder.go        — WeaveJobTemplate → batch/v1 Job spec (incl. codeSource init container)

  deploybuilder/
    builder.go         — WeaveServiceTemplate → apps/v1 Deployment + Service + Ingress;
                          chain-owned (Build/BuildService/BuildIngress) and run-owned
                          (BuildFromOverride/BuildServiceFromOverride/BuildIngressFromOverride)
    names.go           — deterministic resource name helpers, incl. IngressHost()

  codesource/
    codesource.go      — shared helpers: EnvVars() (WEAVE_* + runner.args), WritableVolumeName(),
                          TruncateK8sName(), HasVolume() — used by both builders and jobbuilder

  indexclient/
    client.go           — minimal HTTP client for fusion-index: ResolveTag, FetchAppMetadata,
                           FetchAppMetadataAndVersion, AppMetadata/AppRunner/AppIngress types

  security/
    defaults.go         — Defaults struct (PodAnnotations/PodLabels/PodSecurityContext/
                           ContainerSecurityContext), populated once at startup from
                           WORKLOAD_SECURITY_DEFAULTS

  backup/
    dump.go             — DumpObjects(): lists+strips+YAML-marshals the 4 spec-only kinds
    restore.go           — HasExistingObjects(), RestoreObjects(): kind-dispatch + Create()
    s3.go                — NewS3Client, UploadStream, DownloadStream, FindLatestBackupKey

  trigger/
    cron.go             — CronScheduler (wraps robfig/cron, upsert/remove by key)
    webhook.go            — WebhookServer (chi HTTP server, bearer token auth)
    batchjobs.go          — BatchJob type + ParseBatchJobs() (YAML → validated job list)
    batchcron.go          — BatchCronScheduler: one goroutine + min-heap per trigger
    kafka.go               — KafkaConsumer: one goroutine per trigger, segmentio/kafka-go
    s3event.go              — parseS3EventEnvVars(): MinIO S3 Kafka notification → env vars

  apiserver/
    server.go          — Config, Server, Start()
    router.go            — chi router, health routes, /api/v1 sub-router, /monitor/v1 sub-router
    types.go              — APIError exported type
    auth/                  — APIKeyValidator, OIDCValidator, SAValidator, Authenticator
    middleware/             — Recovery, Logging, Auth (sync.Once), RBAC
    handlers/                — ResourceHandler interface + 5 CRD handler structs

  monitoring/
    config.go           — Config struct (Namespace, Client, KubeClient, CacheTTL, MaxLogLines, Sink)
    routes.go             — RegisterRoutes() wires all 10 GET routes onto a chi sub-router
    metrics_server.go       — standalone http.Server on METRICS_ADDR serving promhttp.Handler()
    cache/
      cache.go               — generic TTLCache[K, V] with lazy eviction (RWMutex)
    logsink/
      sink.go                 — Sink interface + LogSnapshot type + NoopSink
      kafka.go                  — KafkaSink: buffered channel + drainLoop goroutine, stop/done shutdown
    handlers/
      base.go                   — Base struct (shared deps) + cacheGet/writeJSON/writeError helpers
      metrics.go                  — promauto Prometheus metrics (requests, duration, cache hits, run phase gauge)
      runs.go                      — RunsHandler: List (summaries) + Get (run+jobs+events detail)
      jobs.go                       — JobsHandler: List + Get batch/v1 Jobs for a run
      logs.go                        — LogsHandler: pod log snapshot + async sink.Publish
      events.go                       — EventsHandler: events for a run + all events with fieldSelector
      deployments.go                   — DeploymentsHandler: Deployments owned by a chain
      stats.go                          — StatsHandler: RunStats + ChainStats with window filter

config/
  crd/bases/         — generated CRD YAML (kubectl apply target)
  rbac/              — ServiceAccount, Role, RoleBinding for operator, API server, and backup CronJob
  manager/           — raw-YAML Deployments for quick iteration
  samples/           — example CRD instances

deployment/fusion-weave/   — Helm chart (incl. backup-cronjob.yaml, showroom/ demo chains)
```

---

## Operator internals

### Controller-runtime manager

`cmd/main.go` creates a single `controller-runtime` manager scoped to the `fusion` namespace (via `cache.Options{DefaultNamespaces: ...}`). The manager runs all reconcilers in the same process.

Shared components created before reconciler registration:

```
CronScheduler        ──────────┐
BatchCronScheduler   ──────────┤
KafkaConsumer        ──────────┤──► fireCh / batchFireCh / kafkaFireCh
WebhookServer        ──────────┤          │
                                │   WeaveTriggerReconciler
                                │     ◄── drainFireChannel goroutine
                                │     ◄── drainBatchFireChannel goroutine
                                │     ◄── drainKafkaFireChannel goroutine
```

Three independent buffered channels feed the trigger reconciler: `fireCh` (Cron/Webhook, size 64), `batchFireCh` (BatchCron, size 4096 — sized for many jobs firing in the same tick), and `kafkaFireCh` (Kafka, size 1024). Each has its own drain goroutine that stores the request in a per-channel pending map and wakes the reconciler via a `source.Channel` + `WatchesRawSource`. This is necessary because controller-runtime's reconcile loop is not re-entrant. When any of these channels is full, the producer drops the request rather than blocking — the next tick/message re-fires; BatchCron and Kafka document this explicitly since their producers are goroutines that must never stall.

`cmd/main.go` also resolves several env vars once at startup and threads them into the WeaveChain and WeaveRun reconcilers: `INGRESS_HOST_SUFFIX`, `CODE_SOURCE_POLL_INTERVAL` (default `60s`), `FUSION_INDEX_URL`, `LOADER_IMAGE`, `WRITABLE_PATHS` (`:`-separated), and `WORKLOAD_SECURITY_DEFAULTS` (JSON-decoded into `security.Defaults`).

### WeaveJobTemplate / WeaveServiceTemplate reconcilers

Simple validation-only reconcilers. They parse the template spec, check invariants (image required, ports non-empty, resource quantities parseable, ingress rules valid), and write a `valid: true/false` condition to status. A WeaveServiceTemplate with `spec.ingress` set is invalid until the operator-wide `ingress.hostSuffix` is configured. They do not create any Kubernetes workloads — that is the run reconciler's job.

### WeaveChain reconciler

Validates the chain at admission time:
- Calls `dag.BuildGraph` to detect cycles and missing dependency references
- For every step with `consumesOutputFrom`, verifies each named producer is an ancestor with `producesOutput: true`
- Validates `sharedStorage.size` is a parseable resource quantity

After initial validation the chain reconciler runs two independent per-reconcile passes over `chain.Status.ActiveDeployments` (chain-owned deployments only — run-owned deployments from `stepOverrides` are handled entirely by the run controller, see below):

1. **`handleCodeReload`** — processes the one-shot `fusion-platform.io/reload-deploy-step: <stepName>@<version>` annotation (external callers: CI/CD, REST API, webhook) by calling `triggerCodeReload` and consuming the annotation.
2. **`syncDeploymentHealth`** — for each active entry: checks `Available` condition (`Healthy`/`Unhealthy`), tracks how long it has been unhealthy, and rolls back via `rollbackDeployment` (fetch previous ReplicaSet revision, patch `Deployment.Spec.Template` back to it) once `unhealthyDuration` is exceeded. **For entries with `CodeSourceArtifact` set**, the same pass polls fusion-index (`indexclient.ResolveTag`) every `CodeSourcePollInterval` and calls `triggerCodeReload` when the tracked tag has moved to a new version — updating `CodeSourceDeployedVersion` so the next poll doesn't re-fire.

The health loop deliberately uses `if`/`else` (not `continue`) between the health check and the code-source check so code-source polling runs for every entry regardless of deployment health.

The chain reconciler watches `apps/v1 Deployments` with a label-based handler: when a Deployment changes, the reconciler is enqueued for the WeaveChain identified by the `fusion-platform.io/chain` label.

### WeaveTrigger reconciler and the fire channel

The trigger reconciler manages the lifecycle of cron jobs, webhook routes, batch schedulers, and Kafka consumers:

```
Reconcile() called
  └── type = Cron       → CronScheduler.Upsert(key, schedule, callback)
  └── type = OnDemand   → check annotation fusion-platform.io/fire=true
  └── type = Webhook    → WebhookServer.Register(path, token, callback)
  └── type = BatchCron  → syncBatchCronSource(): fetch ConfigMap "jobs.yaml" key,
                           ParseBatchJobs(), BatchCronScheduler.Upsert(key, ns, name, jobs)
  └── type = Kafka      → syncKafkaSource(): resolve SASL secret (if any),
                           KafkaConsumer.Upsert(key, ns, name, cfg)
```

When a cron tick or webhook POST fires the callback:
1. The callback writes a `FireRequest{TriggerName, TriggerNamespace, Overrides}` to `fireCh`
2. `drainFireChannel()` goroutine reads from `fireCh`, stores the request in `pendingFires` (keyed by `namespace/name`), and sends a `GenericEvent` to `wakeupCh`
3. The `source.Channel` watcher enqueues the trigger for reconciliation
4. On the next reconcile the trigger controller reads `pendingFires`, applies `concurrencyPolicy` (Allow / Wait), and creates a `WeaveRun` object

For **OnDemand** triggers, the annotation is detected directly in `Reconcile()` — no channel hop needed because the annotation change already triggers a reconcile via the standard watch.

On `NotFound` (trigger deleted), the reconciler also calls `BatchCronScheduler.Remove(key)` and `KafkaConsumer.Remove(key)` alongside the existing `CronScheduler`/`WebhookServer` cleanup.

### BatchCron and Kafka trigger internals

**BatchCron** (`internal/trigger/batchjobs.go`, `batchcron.go`): the ConfigMap's `jobs.yaml` key holds a YAML sequence of job entries, each with `id`, `schedule` (5-field standard cron, parsed by `standardCronParser`, no seconds field — distinct from the 6-field seconds-first format used by plain `Cron` triggers), optional `startdate`/`starttime` (a `NotBefore` floor), and free-form `metadata` (JSON-encoded into `JOB_METADATA`). `ParseBatchJobs` returns valid jobs plus per-entry `ValidationError{Line, Message}` for anything malformed — one bad entry doesn't invalidate the whole list. Every job gets 8 standard env vars: `JOB_ID`, `JOB_NAME`, `JOB_TOPIC`, `JOB_MAINTAINER`, `JOB_STARTDATE`, `JOB_STARTTIME`, `JOB_SCHEDULE`, `JOB_METADATA`.

`BatchCronScheduler` runs **one goroutine per trigger** (`batchRunner.run`), each driving a `container/heap`-based min-heap of `(nextFireTime, *BatchJob)` pairs:

```
batchRunner.run() loop:
  timer fires at heap[0].next (or 1h idle poll if heap empty)
    → pop all due entries, send BatchFireRequest per entry (non-blocking; dropped if fireCh full)
    → reschedule each popped job via job.Schedule.Next(now), push back onto heap
  updateCh receives new []BatchJob (from Upsert)
    → replace the whole heap in place — the goroutine itself is NOT restarted
  stopCh closed → return
```

`Upsert` on an existing key writes to a buffered(1) `updateCh` (draining any stale pending update first) rather than tearing down and recreating the goroutine — so re-editing the ConfigMap doesn't cause a scheduling gap for jobs whose entries didn't change.

**Kafka** (`internal/trigger/kafka.go`, `s3event.go`): `KafkaConsumer` runs one `kafkaRunner` goroutine per trigger, each owning a `kafka-go` `Reader` with manual offset commits (`CommitInterval: 0`). `Upsert` on an existing key stops the old runner (`context.CancelFunc`) before starting a new one — full goroutine replacement, unlike BatchCron's live heap swap. `buildDialer` configures SASL (`PLAIN`, `SCRAM-SHA-256`, or `SCRAM-SHA-512`, default `PLAIN`) only when `SASLUsername` is non-empty.

Each consumed message is parsed by `parseS3EventEnvVars` as a MinIO S3 Kafka notification envelope (`{"EventName", "Records": [{"eventName", "eventTime", "s3": {"bucket", "object"}}]}`). `EventFilter` (`put`/`delete`/`get`, matched against `s3:ObjectCreated`/`s3:ObjectRemoved`/`s3:ObjectAccessed` prefixes) and `BucketFilter` (exact bucket name match) gate whether a `KafkaFireRequest` is produced; **the Kafka offset is always committed regardless of filter outcome** — a filtered-out or throttled message is consumed and discarded, never redelivered. On a filter pass, 7 env vars are attached: `S3_EVENT_NAME`, `S3_BUCKET`, `S3_KEY`, `S3_SIZE`, `S3_ETAG`, `S3_EVENT_TIME`, `S3_EVENT_JSON` (the raw payload).

`maybeCreateKafkaRun` additionally enforces `spec.kafka.maxConcurrentRuns` by counting non-terminal WeaveRuns for the chain; over the cap, the fire is silently dropped (offset already committed, so no redelivery/backlog builds up).

### WeaveRun reconciler — the execution engine

The run reconciler is the most complex component. Its `Reconcile()` method is called every time a WeaveRun or a watched child resource (Job, Deployment) changes. Because it is idempotent and edge-triggered, it drives the DAG forward incrementally across many calls.

**Each reconcile cycle:**

```
1.  Get WeaveRun                          (return if terminal)
2.  Get WeaveChain
3.  Resolve auth secret name (run override → trigger override → chain default)
4.  Ensure shared PVC (if sharedStorage configured)
5.  Load all referenced job/service templates
6.  dag.BuildGraph(chain.Spec.Steps)
7.  Snapshot current step states from run.Status.Steps
8.  Sync running steps:
      Job steps    → check batch/v1 Job conditions (Complete/Failed)
      Deploy steps → check Deployment Available condition;
                      once Deployed, run-owned steps poll code-source here
                      (pollRunDeploymentCodeSource), chain-owned steps defer
                      to the chain controller
      Capture stdout output for completed producing steps
9.  dag.Advance(graph, states, failurePolicy)
10. For each DecisionStart:
      Job step            → jobbuilder.Build() → client.Create(Job)
      Deploy step (no override)  → syncDeployStep() → chain-owned <chainName>-<stepName>
      Deploy step (stepOverrides) → syncDeployStepFromOverride() → run-owned <runName>-<stepName>
11. Write updated status (steps, phase, completion time)
```

The reconciler watches `batch/v1 Jobs` and `apps/v1 Deployments` via label-based handlers: when a Job completes, the handler maps it back to the owning WeaveRun and enqueues it. This ensures the reconciler is woken up promptly without polling.

**Optimistic concurrency:** `client.MergeFrom(run.DeepCopy())` is captured immediately after `r.Get()`, before any mutations, so status patches always diff against the last-read version. If two concurrent reconciles race, the second will receive a conflict error and be requeued.

**`dag.Advance` is called twice** per reconcile: once before the decisions loop (to get the decisions that drive step creation) and once after (to recompute `RunComplete` using the post-decision `stepStates`). The second call is required — without it, deploy steps cause the WeaveRun to complete immediately on first reconcile, since the pre-decision states map has no entry for the just-started step.

---

## DAG engine

`internal/dag` is a pure-Go package with zero Kubernetes dependencies. It can be unit-tested without a cluster.

### graph.go — construction

`BuildGraph(nodes []Node)` builds a `Graph` by:
1. Registering all nodes in a map (duplicate name → error)
2. Validating all `DependsOn` references resolve (unknown dep → error)
3. Running **Kahn's topological sort** (O(V+E)):
   - Compute in-degree for each node
   - Seed a queue with all zero-in-degree nodes (roots)
   - Process queue: append to order, decrement in-degree of dependents, enqueue newly zero-in-degree nodes
   - If `len(order) != len(nodes)` → cycle detected

The resulting topological order is stored in the `Graph` and returned by `Nodes()`.

### executor.go — advancement

`Advance(graph, states, policy)` is a **pure function** — it reads the current step phases and returns a map of `StepDecision` values without mutating anything.

Decision logic per step (evaluated in topological order):

```
Already terminal (Succeeded/Failed/Skipped)  → DecisionTerminal
Currently running (Running/Retrying/Deployed) → DecisionWait
StopAll in effect (any step failed)          → DecisionSkip
Any dependency not yet terminal              → DecisionWait
conditionMet (runOnSuccess/runOnFailure)     → DecisionStart
Otherwise                                    → DecisionSkip
```

`conditionMet` checks `runOnSuccess` and `runOnFailure` against the terminal states of direct dependencies:
- `runOnSuccess=true` → start if ALL deps succeeded
- `runOnFailure=true` → start if ANY dep failed
- Both can be true simultaneously (step runs regardless of upstream outcome)

The overall run is **complete** when no step is in `DecisionWait`, and **succeeded** when complete and no step failed. A step in `StepPhaseDeployed` is non-terminal for this purpose — `RunComplete` is never true while any step is `Deployed` (see [Deploy steps and health monitoring](#deploy-steps-and-health-monitoring)).

---

## Job builder

`internal/jobbuilder/builder.go` translates a `WeaveJobTemplate` + chain step + run into a complete `batch/v1 Job` spec.

Key naming conventions:
- Job name: `<runName>-<stepName>-<retryCount>` (deterministic, retry-safe), routed through `codesource.TruncateK8sName(name, 63)` — `batch/v1 Job` names must fit 63 bytes (not the generic 253-byte object-name limit) because Kubernetes auto-derives a `job-name` pod-template label from it, and label values are capped at 63 bytes
- Output ConfigMap: `<runName>-outputs`
- Shared PVC: `<runName>-shared`
- Input ConfigMap key: `input-<stepName>`
- Output ConfigMap key: `step-<stepName>`

Standard labels applied to every Job and pod:
- `fusion-platform.io/run` = run name
- `fusion-platform.io/chain` = chain name
- `fusion-platform.io/step` = step name

These labels allow the run reconciler's watch handler to map Job events back to the correct WeaveRun.

The builder also wires up:
- **Env overrides** from the chain step (merged over the template's base env)
- **Input volume** (`/weave-input/input.json`) when `consumesOutputFrom` is set — mounted from the run's output ConfigMap
- **Shared volume** (`/weave-shared`) when the chain has `sharedStorage` — mounted from the per-run PVC
- **`authSecretRef` envFrom** — when resolved by the run controller, injected into the job container
- **`code-loader` init container** — when `template.Spec.CodeSource` is set, mirroring the Deploy-builder mechanism (see [Code-source artifact loading](#code-source-artifact-loading)); `Build`'s caller passes pre-fetched `csMeta`/`csVersion` (nil/empty when the fetch failed or CodeSource is unset) so metadata-derived env vars degrade gracefully rather than failing the whole job build
- **`WRITABLE_PATHS` emptyDir volumes** — one per configured writable path, sanitized via `codesource.WritableVolumeName`, mounted into both the job container and the `code-loader` init container when CodeSource is configured

---

## Deploy builder

`internal/deploybuilder/builder.go` translates a `WeaveServiceTemplate` into:
- `apps/v1 Deployment` with a `RollingUpdate` strategy
- `corev1.Service` of the configured type
- `networking.k8s.io/v1 Ingress` (only when `spec.ingress` is set)

It exposes **two parallel families of build functions** with an important behavioral asymmetry:

| | Chain-owned | Run-owned (stepOverrides) |
|---|---|---|
| Deployment | `Build` | `BuildFromOverride` |
| Service | `BuildService` | `BuildServiceFromOverride` |
| Ingress | `BuildIngress` | `BuildIngressFromOverride` |
| Name | `<chainName>-<stepName>` | `<runName>-<stepName>` |
| Owner | WeaveChain | WeaveRun |
| Runner config source | `tmpl.Spec` (+ optional CodeSource metadata for env vars only) | Always resolved live from fusion-index (`indexclient.FetchAppMetadataAndVersion`) via the override's `artifactName`/`tag` |
| Ports | Always from `tmpl.Spec.Ports` — `Build` writes `WEAVE_PORT` from `meta.Runner.Port` but never replaces container/service ports | Replaces ports entirely when `meta.Runner.Port > 0` |

Do not assume `Build` and `BuildFromOverride` behave the same for ports — this was a deliberate but easy-to-miss divergence.

**Resource naming:** both name families are stable across runs — `Build`'s output is the same for every run of a chain (enabling in-place rolling updates of one shared Deployment), while `BuildFromOverride`'s output is the same across reconciles of one specific WeaveRun but distinct per run (enabling multiple concurrent runs of the same chain to each get an isolated Deployment). Both route through `stepResourceName` → `codesource.TruncateK8sName(name, 63)` (Service names must fit 63 bytes; Deployment/Ingress use the same truncated value so all three stay linked for long names rather than only the Service silently diverging).

**Immutable selector labels:**
```
fusion-platform.io/chain = <chainName>
fusion-platform.io/step  = <stepName>
```

The run name is deliberately excluded from selector labels even for run-owned Deployments — this keeps the Deployment/Service selector free of Kubernetes' "immutable after creation" trap, since a resource name embedding the run name already disambiguates without needing the label to do so too.

**Owner reference:** chain-owned Deployments are owned by the **WeaveChain** — they survive run deletion and continue serving traffic; only deleting the WeaveChain (or the chain step) garbage-collects them. Run-owned Deployments (via `stepOverrides`) are owned by the **WeaveRun** — see [Run-owned deployments](#run-owned-deployments-weaverunspecstepoverrides) for their teardown path, which is explicit (`doDeployTeardown`), not pure GC, because terminal runs keep a finalizer until cleanup completes.

**Ingress hostname:** `deploybuilder.IngressHost(name, hostSuffix)` (`internal/deploybuilder/names.go`) is the single place that joins a user-supplied DNS label with the cluster-wide suffix. `WeaveIngressRule.name` (template) and `WeaveRunStepOverride.ingressName` (run override) are both DNS-label-only fields (`+kubebuilder:validation:Pattern`) — free-text hostnames were removed. The operator always appends `ingress.hostSuffix` (Helm value) / `INGRESS_HOST_SUFFIX` (env var) to form the real host, so neither a Flux-managed template nor a REST-API-created run can point an Ingress at a hostname the operator doesn't own. When `hostSuffix` is empty, `IngressHost` returns `name` unchanged, but both `syncDeployStep` and `syncDeployStepFromOverride` refuse to proceed (return an error, failing just that step) if an Ingress is configured and `IngressHostSuffix == ""` — a `WeaveServiceTemplate` with `spec.ingress` set is additionally marked `status.valid=false` at admission time until the suffix is configured cluster-wide.

---

## Code-source artifact loading

Both `WeaveJobTemplateSpec.CodeSource` and `WeaveServiceTemplateSpec.CodeSource` (`*CodeSourceSpec`) let a step pull a versioned build artifact from `fusion-index` instead of baking it into the container image. The mechanism is identical for Job and Deploy steps; only the refresh cadence differs.

```
┌────────────────────────────────────────────────────────────────────┐
│  Pod created (Job or Deploy step) with codeSource configured         │
│                                                                      │
│  code-loader init container (cmd/loader/main.go, binary at /loader)  │
│  env: INDEX_URL, ARTIFACT_NAME, ARTIFACT_TAG, MOUNT_PATH             │
│    1. GET /api/v1/artifacts?name=<ARTIFACT_NAME>        → artifactID │
│    2. GET /api/v1/artifacts/{id}/versions                            │
│         find the version whose tags include ARTIFACT_TAG             │
│    3. GET .../versions/{version}/files                → file list    │
│    4. GET .../files/{fileID}/download   (per file)                   │
│         write each file as-is to MOUNT_PATH/<basename> — NO archive   │
│         extraction; the runner image handles that itself              │
│    5. write MOUNT_PATH/.version = resolved semver (from fusion-index, │
│         not from any content inside metadata.yaml)                    │
│                                                                        │
│  Main container starts, reads code from MOUNT_PATH                    │
└────────────────────────────────────────────────────────────────────┘
```

The loader fires on **every pod start** — including the first start of a new run — so a Job step always gets the tag's current version at run time with no separate refresh mechanism needed.

**Env var injection** (`internal/codesource/codesource.go` `EnvVars()`, called by both builders and the job builder) — every container with `codeSource` receives:

| Always present | Present when metadata was fetched successfully |
|---|---|
| `WEAVE_ARTIFACT`, `WEAVE_TAG`, `WEAVE_VERSION`, `WEAVE_NAMESPACE`, `WEAVE_MOUNT_PATH` | `WEAVE_PORT`, `WEAVE_INGRESS_PATH_PREFIX`, `WEAVE_RUNNER_TYPE`, `WEAVE_BUILDER_IMAGE`, `WEAVE_MAINTAINER`, plus every `runner.args` key from `metadata.yaml` as a plain env var |

`internal/indexclient/client.go` is the shared HTTP client used by the operator (not the init container binary, which has its own minimal copy in `cmd/loader/main.go` to avoid an extra binary dependency): `ResolveTag` returns just the semver string; `FetchAppMetadataAndVersion` additionally downloads and parses `metadata.yaml` (via `sigs.k8s.io/yaml`) into an `AppMetadata{Runner, Ingress, Resources, Maintainer}` struct in one round-trip-efficient call. `runner.args` in `metadata.yaml` is a **map**, not a list of `{name, value}` objects — a schema detail worth calling out because it's easy to assume list-style like `env`. `runner.args` keys that collide with `WeaveServiceTemplateSpec.Env`/`WeaveJobTemplateSpec.Env` entries produce silent duplicate env vars (the builder appends without deduplication); template env comes first, so runner.args effectively wins at container runtime (last value wins).

**Refresh cadence differs by step kind:**
- **Job steps** — no polling needed. Each WeaveRun creates a fresh Job pod, so the loader resolves the tag fresh every time.
- **Deploy steps (chain-owned)** — the WeaveChain reconciler's `syncDeploymentHealth` polls fusion-index every `CodeSourcePollInterval` (default 60s, `CODE_SOURCE_POLL_INTERVAL` env var) for every `chain.Status.ActiveDeployments` entry that has `CodeSourceArtifact` set, and calls `triggerCodeReload` — which patches `kubectl.kubernetes.io/restartedAt` plus `fusion-platform.io/code-source-version` onto `Deployment.Spec.Template.Annotations` and calls `deploybuilder.UpdateVersionEnvVar` to keep `WEAVE_VERSION` accurate in the new pods — whenever the resolved version differs from `CodeSourceDeployedVersion`.
- **Deploy steps (run-owned via stepOverrides)** — the same polling logic runs inside the **WeaveRun** reconciler (`pollRunDeploymentCodeSource`), against `run.Status.ActiveDeployments` instead of the chain's.
- **External trigger** — the `fusion-platform.io/reload-deploy-step: <stepName>@<version>` annotation on the WeaveChain is a one-shot manual override for the chain-owned path, consumed by `handleCodeReload`; there is no run-owned equivalent annotation (run-owned deployments are refreshed purely by the poll loop).

`WRITABLE_PATHS` (operator env var, `codeSource.writablePaths` in Helm, `:`-separated, default `/tmp:/home/nonroot:/weave-work`) mounts a writable `emptyDir` at each path in both the `code-loader` init container and the main container — but **only when `codeSource` is configured** on the step — required when `readOnlyRootFilesystem: true` is in effect so runners can write to `/tmp`, install dependencies, etc.

---

## Run-owned deployments (WeaveRun.spec.stepOverrides)

`WeaveRunSpec.StepOverrides []WeaveRunStepOverride` lets a specific run instantiate a deploy-kind step as its **own** Deployment/Service/Ingress instead of sharing the chain's single Deployment. Each override entry names the target `stepName` plus `artifactName`/`tag` (resolved live from fusion-index — the WeaveServiceTemplate's own `image`/`ports` are not used for the container image or ports in this path) and an optional `ingressName`.

```
DecisionStart for a Deploy-kind step
  │
  ▼
findStepOverride(run.Spec.StepOverrides, stepName)
  │                                        │
  no override                              override found
  ▼                                        ▼
syncDeployStep()                    syncDeployStepFromOverride()
  chain-owned, stable across runs      run-owned, one per WeaveRun
  <chainName>-<stepName>               <runName>-<stepName>
  owner: WeaveChain                    owner: WeaveRun
  deploybuilder.Build/BuildService/    indexclient.FetchAppMetadataAndVersion()
    BuildIngress                       deploybuilder.BuildFromOverride/
                                          BuildServiceFromOverride/
                                          BuildIngressFromOverride
  │                                        │
  ▼                                        ▼
health tracked in                    health tracked in
chain.Status.ActiveDeployments       run.Status.ActiveDeployments
  → chain controller polls             → run controller polls
    (syncDeploymentHealth)               (pollRunDeploymentCodeSource,
                                           inline in Reconcile's step-sync loop)
```

Once the run-owned Deployment reaches `Available`, the step transitions to `StepPhaseDeployed` exactly like the chain-owned path (`isDeploymentAvailable`), and `registerRunActiveDeployment` records it in `run.Status.ActiveDeployments` with `CodeSourceArtifact`/`Tag`/`IndexURL` cached from the override so `pollRunDeploymentCodeSource` doesn't need to re-read the WeaveRun spec on every poll.

**Teardown:** `doDeployTeardown` (invoked via the `weave.fusion-platform.io/deploy-cleanup` finalizer on run deletion or terminal-phase entry) iterates `run.Status.Steps` and, for every step with a `DeploymentRef`, deletes the Deployment/Service/Ingress by name and removes the corresponding entry from **both** `chain.Status.ActiveDeployments` (if the entry happens to be there — chain-owned case) and `run.Status.ActiveDeployments` (run-owned case) — the same teardown code path handles both ownership models because it only ever looks at the run's own step statuses, not at which map the entry originated in.

Because `WeaveRunStepOverride.ingressName` is a DNS label (not free text), it goes through the same `IngressHost(name, hostSuffix)` join as the template path; `syncDeployStepFromOverride` has its own runtime guard (`r.IngressHostSuffix == ""` check) equivalent to the template-validation guard in the chain-owned path, since an override-only Ingress bypasses WeaveServiceTemplate admission validation entirely.

---

## Step output passing

Steps opt in to output production and consumption in the chain spec:

```
step A: producesOutput: true
step B: consumesOutputFrom: [A]
```

**Capture path (after Job A completes):**
1. Run reconciler reads the last valid JSON line from the Job's pod stdout via the Kubernetes Logs API
2. Writes the captured JSON into key `step-A` in ConfigMap `<runName>-outputs`
3. Sets `WeaveRunStepStatus.OutputCaptured = true` to prevent double-capture on requeue

**Injection path (before Job B starts):**
1. `prepareInputData()` reads all producer outputs from the ConfigMap
2. Builds a merged JSON object namespaced by step name: `{"A": {...from step A...}}`
3. Writes the merged JSON into key `input-B` in the same ConfigMap
4. Returns `(cmName, ready=true, nil)` — if any producer has not yet captured, returns `ready=false` to requeue

**At Job B runtime:**
The job pod has the ConfigMap key `input-B` mounted as `/weave-input/input.json`. The application reads this file to access upstream data.

Namespacing by producer name (`{"A": ...}`) prevents key collisions when a step consumes from multiple producers.

---

## Shared storage

When `spec.sharedStorage` is set on a WeaveChain, the run reconciler provisions a `PersistentVolumeClaim` named `<runName>-shared` with `accessModes: [ReadWriteMany]` once per run.

- Owned by the WeaveRun → garbage-collected automatically when the run is deleted
- Mounted at `/weave-shared` in every job pod in the run (via the job builder)
- Suitable for large artifacts that cannot fit in JSON stdout

The StorageClass must support `ReadWriteMany`. On minikube this requires the `csi-hostpath-driver` addon.

---

## Deploy steps and health monitoring

When the run reconciler encounters a `DecisionStart` for a `stepKind: Deploy` step:

1. `deploybuilder.Build()` (or `BuildFromOverride()` for a `stepOverrides` entry) constructs the Deployment, Service, and optional Ingress specs
2. The reconciler creates the Deployment if absent, or patches `Spec.Template`/`Spec.Replicas` in place for a rolling update; Service/Ingress use a similar get-then-create-or-patch upsert (`upsertService`/`upsertIngress`)
3. Owner reference set to **WeaveChain** (chain-owned) or **WeaveRun** (run-owned via `stepOverrides`)
4. The step is marked `Running` with `DeploymentRef` pointing to the Deployment name

On subsequent reconcile cycles the run reconciler checks `isDeploymentAvailable()`, which looks for `DeploymentAvailable condition = True` in the Deployment status. When available:
- Step phase → `Deployed` (**not** `Succeeded` — see below)
- Chain-owned: `registerActiveDeployment()` patches `WeaveChain.Status.ActiveDeployments` to register this step for ongoing health monitoring by the chain controller
- Run-owned: `registerRunActiveDeployment()` populates `WeaveRun.Status.ActiveDeployments` instead, monitored by the run controller itself

`StepPhaseDeployed` is **non-terminal**: it satisfies dependency checks for downstream steps (they can start once the service is up) but keeps the owning WeaveRun in `Running` phase for the lifetime of the service — `RunComplete` is never true while any step is `Deployed` (`dag/executor.go`). Deploy step lifecycle: `Pending → Running → Deployed`; transitions to `Failed` only if the Deployment is deleted externally, or (new) if the run reconciler detects the pod is crash-looping before availability is first reached (`deploymentPodFailureMessage`) — this catches misconfiguration (e.g. a bad loader URL) without waiting indefinitely for `Available`.

**Post-run health monitoring (WeaveChain reconciler, chain-owned deployments only):**

`syncDeploymentHealth()` runs on every chain reconcile and iterates `ActiveDeployments`:

```
Available=True  → phase = Healthy (or stays Healthy)
Available=False → phase = Unhealthy, record timestamp
Unhealthy for > unhealthyDuration → phase = RollingBack
  → rollbackDeployment(): fetch all ReplicaSets by label,
    find revision N-1, patch Deployment.Spec.Template back to it
After rollback → phase = RolledBack
(independently, for entries with CodeSourceArtifact set:
  poll fusion-index tag → triggerCodeReload on version change)
```

The chain reconciler watches Deployments via a label-based handler (`fusion-platform.io/chain`) so it is notified immediately when a Deployment's availability changes. **Run-owned Deployments are not watched or health-monitored by the chain controller at all** — the run controller inline-polls them as part of its normal step-sync loop instead (see [Run-owned deployments](#run-owned-deployments-weaverunspecstepoverrides)); there is no automatic rollback for run-owned deployments, only code-source version tracking.

`weave.fusion-platform.io/deploy-cleanup` finalizer is added to any WeaveRun whose chain has deploy-kind steps; on deletion or entering a terminal phase (`Succeeded`/`Failed`/`Stopped`) it runs `doDeployTeardown` (deletes Deployment + Service + Ingress, removes the `ActiveDeployments` entry from whichever of chain/run status holds it) before allowing GC to remove the finalizer. Succeeded runs are exempt from the *delete* half of teardown for chain-owned steps — the Deployment survives so future runs on the same chain can rolling-update it in place — but a Succeeded run's finalizer logic still runs once to reconcile status; run-owned Deployments, by contrast, are always deleted since they belong to that one run.

---

## Pod/container security defaults

`internal/security/defaults.go` defines `security.Defaults{PodAnnotations, PodLabels, PodSecurityContext, ContainerSecurityContext}`. The zero value injects nothing. The operator populates one instance at startup from the `WORKLOAD_SECURITY_DEFAULTS` env var (JSON, wired to `security.defaultPodSecurityContext`/`security.defaultContainerSecurityContext` in Helm) and passes it into both `jobbuilder.Build` and `deploybuilder.Build`/`BuildFromOverride`.

**Per-template override:** `WeaveJobTemplateSpec.PodSecurityContext`/`ContainerSecurityContext` and `WeaveServiceTemplateSpec.PodSecurityContext`/`ContainerSecurityContext` — when set on a specific template, they **replace** the operator-wide default entirely for that template's pods (not merged field-by-field). Builder pattern: each builder computes `podSC`/`containerSC` local variables at the top of the function (`if tmpl.Spec.X != nil { use tmpl.Spec.X } else { use security defaults }`), then references those variables in the pod/container struct literals. `ContainerSecurityContext` applies to both the main container and the `code-loader` init container on Deploy steps with `codeSource` configured.

---

## Backup and restore

A separate one-shot binary (`cmd/backup/`, built into the same image as `/manager` and `/api-server`, invoked as `/backup`) dumps or restores the four **spec-only** CRD kinds — `WeaveJobTemplate`, `WeaveServiceTemplate`, `WeaveChain`, `WeaveTrigger` — to/from S3. `WeaveRun` is **never** included: it's transient execution state, not something a restore should recreate. `.status` is always zeroed before marshaling on the dump side.

```
backup (default, no args)                    backup restore
  │                                             │
  DumpObjects(ctx, client, ns, gzWriter)        HasExistingObjects() check
    List() each of the 4 kinds in order:          → refuse unless RESTORE_FORCE=true
      JobTemplate → ServiceTemplate                  (templates before the chain that
      → Chain → Trigger                               references them, etc. — no ordering
    for each item:                                    dependency on restore since Create()
      set TypeMeta explicitly (client.List             doesn't validate cross-refs at admission
      zeroes it, same gotcha as r.Get())               beyond what the chain controller re-checks)
      zero .Status                                  FindLatestBackupKey() if RESTORE_BACKUP_KEY
      stripObjectMeta() — clears                       unset (lexicographic == chronological,
        ResourceVersion/UID/Generation/                 since keys embed a zero-padded UTC
        CreationTimestamp/ManagedFields/                 timestamp)
        SelfLink; keeps Name/Namespace/                DownloadStream() → gunzip
        Labels/Annotations                             RestoreObjects(): read "---"-separated
      write as one YAML doc in a                        docs, dispatch by "kind" field, force
      "---"-separated stream                            Namespace = target namespace (never
    gzip.Writer wraps the stream                        trust the embedded namespace), Create()
    io.Pipe streams gz output straight                    AlreadyExists → Skipped, not fatal
      into S3 multipart upload — no local                other errors → collected, continue
      temp file (size unknown upfront)                    with next document
  UploadStream() to <prefix>/backup-<UTC timestamp>.yaml.gz
```

Env vars (shared by both subcommands, `cmd/backup/config.go`): `NAMESPACE` (default `fusion`), `S3_BUCKET` (required), `S3_BACKUP_PREFIX`, `AWS_REGION` (default `us-east-1`), `S3_ENDPOINT_OVERRIDE` (MinIO/Ceph), `RESTORE_FORCE`, `RESTORE_BACKUP_KEY`, `LOG_LEVEL`/`LOG_FORMAT` — names deliberately mirror `fusion-index`'s `backup-db`/`restore-db` conventions so both projects are operated the same way. `internal/backup/s3.go`'s `NewS3Client` mirrors `fusion-index`'s `internal/storage.NewS3Client` region/endpoint-override rules exactly.

The `main()` client is built with `client.New(ctrl.GetConfigOrDie(), ...)` directly — no manager, no cache, no leader election, since this is a one-shot batch job, not a controller. Deployed as a daily `CronJob` (`deployment/fusion-weave/templates/backup-cronjob.yaml`, gated by `backup.enabled` in `values.yaml`, default schedule `0 3 * * *`) with a dedicated least-privilege ServiceAccount/Role/RoleBinding (`config/rbac/backup-*.yaml` and the Helm-templated equivalents). `restore` is **manual-only** — deliberately not wired to any automatic Helm trigger, so a destructive DR operation is never one `helm upgrade --set` away from accidentally firing.

---

## REST API server

The REST API server is a **separate binary** (`cmd/api/main.go`) that runs alongside the operator but is independently deployable. It speaks directly to the Kubernetes API server — it has no private state or database.

### Request lifecycle

```
HTTP request
  → chi RealIP
  → Recovery (panic → 500)
  → Logging
  └─ /healthz, /readyz   (no auth)
  └─ /api/v1/*
      → Auth middleware (sync.Once lazy init)
          → APIKeyValidator   (Secret label lookup + SHA-256 compare)
          → OIDCValidator     (JWKS discovery, JWT verify, role claim extract)
          → SAValidator       (TokenReview API, SA label role lookup)
      → RBAC middleware (role × HTTP method enforcement)
      → ResourceHandler (List/Create/Get/Update/Patch/Delete)
          → controller-runtime client → Kubernetes API server
```

### Authentication

All three auth modes are tried in order on each request. The first that produces a non-nil `Result` wins. Each mode is independently enabled/disabled.

| Mode | Identity | Role source |
|---|---|---|
| API key | Secret name in namespace | Annotation `fusion-platform.io/role` on Secret |
| OIDC JWT | JWT `sub` claim | Configurable JWT claim (default: `fusion-weave-role`) |
| SA token | ServiceAccount name | Label `fusion-platform.io/role` on SA (default: `viewer`) |

OIDC initialization (JWKS discovery) happens on the first authenticated request, not at server startup, using `sync.Once` to ensure it happens exactly once under concurrent load.

### RBAC

| Role | Permitted methods |
|---|---|
| `viewer` | GET |
| `editor` | GET, POST, PUT, PATCH |
| `admin` | GET, POST, PUT, PATCH, DELETE |

Health endpoints (`/healthz`, `/readyz`) are registered **before** the auth middleware on the root router, ensuring they are never gated regardless of configuration.

### PATCH semantics

`PATCH` uses **JSON Merge Patch** (`application/merge-patch+json`). The handler fetches the current resource (populating `resourceVersion` for optimistic concurrency), then calls `client.Patch` with the raw merge-patch bytes. The API server applies the patch and returns the updated object. Unlike Create/Update — which decode into the typed Go struct and get CRD-validated — Patch forwards the raw JSON body straight to the API server, so a stale/misspelled field name is silently pruned by the structural schema, not applied and not rejected. Callers can set any metadata field this way, including annotations used for one-shot operator triggers (e.g. `{"metadata":{"annotations":{"fusion-platform.io/restart-step":"stepName"}}}`).

---

## Monitoring API server

The monitoring API is served as a sub-router under `/monitor/v1/` on the same `http.Server` and port as the CRUD API. It shares the same auth and RBAC middleware. It is gated behind `MonitoringEnabled` in the server config and only registered when enabled.

### Package layout

```
internal/monitoring/
  config.go          — Config: Namespace, Client, KubeClient, CacheTTL, MaxLogLines, Sink
  routes.go          — RegisterRoutes(): constructs Cache + Base, wires 10 GET routes
  metrics_server.go  — MetricsServer: http.Server on METRICS_ADDR, serves promhttp.Handler()
  cache/
    cache.go         — TTLCache[K comparable, V any]: RWMutex, lazy expiry on Get
  logsink/
    sink.go          — Sink interface, LogSnapshot struct, NoopSink
    kafka.go         — KafkaSink: buffered channel (256), drainLoop goroutine
  handlers/
    base.go          — Base struct + cacheGet/writeJSON/writeError/nameFromURL helpers
    metrics.go       — Prometheus metrics via promauto
    runs.go, jobs.go, logs.go, events.go, deployments.go, stats.go
```

### Request lifecycle

```
GET /monitor/v1/<path>
  → Auth middleware (same as CRUD API)
  → RBAC middleware (viewer role required for all monitoring endpoints)
  → handler.cacheGet(w, key)   — returns 200 from cache on hit (increments cacheHitsTotal)
  → Kubernetes API / kubeClient call on miss
  → cache.Set(key, result)
  → writeJSON(w, 200, result)
```

### In-memory cache

`TTLCache[K, V]` is a generic Go type. `Get` uses `RLock` to check existence and expiry, then re-acquires `WLock` to delete and return `(zero, false)` for expired entries. `Set` uses `WLock`. There is no background eviction goroutine — stale entries are pruned lazily on read.

Cache keys follow a consistent scheme:

| Key pattern | Handler |
|---|---|
| `runs:list` | RunsHandler.List |
| `run:detail:<name>` | RunsHandler.Get |
| `run:jobs:<name>` | JobsHandler.List |
| `run:job:<name>:<jobName>` | JobsHandler.Get |
| `run:logs:<name>:<step>` | LogsHandler.Get |
| `run:events:<name>` | EventsHandler.ListForRun |
| `events:all:<fieldSelector>` | EventsHandler.ListAll |
| `chain:deployments:<name>` | DeploymentsHandler.List |
| `stats:runs:<window>` | StatsHandler.RunStats |
| `stats:chain:<name>:<window>` | StatsHandler.ChainStats |

### Kafka log sink

```
LogsHandler.Get()
  └─ fetches log snapshot from Kubernetes
  └─ go sink.Publish(context.Background(), snap)   ← fire-and-forget goroutine
         │
         └─ KafkaSink.Publish()
               └─ select { case ch <- snap ; case <-stop: return ErrClosed }
                         │
                    drainLoop goroutine
                         └─ kafka-go writer.WriteMessages()
```

`KafkaSink.Close()` closes the `stop` channel and blocks on `<-done` until the drain loop flushes all buffered snapshots and closes the Kafka writer. If Kafka is not configured, `NoopSink` is used transparently. (This is a separate, independent `kafka-go` writer from the `trigger.KafkaConsumer` reader used for `Kafka`-type WeaveTriggers — the monitoring sink and the trigger consumer share no code or connections.)

### Prometheus metrics server

A separate `http.Server` is started on `METRICS_ADDR` (default `:9091`) when `MetricsAddr` is non-empty. It serves only `promhttp.Handler()` at `/metrics` with no auth middleware. Its lifecycle is tied to the parent `context.Context` passed to `Server.Start()`.

Metrics registered by `internal/monitoring/handlers/metrics.go`:

| Metric | Labels | Description |
|---|---|---|
| `weave_monitor_requests_total` | `path`, `status` | Request count per endpoint |
| `weave_monitor_request_duration_seconds` | `path` | Latency histogram |
| `weave_monitor_cache_hits_total` | — | Cache hits across all handlers |
| `weave_monitor_cache_misses_total` | — | Cache misses across all handlers |
| `weave_runs_by_phase` | `phase` | Current WeaveRun count per phase |

`weave_runs_by_phase` is updated on every call to `RunsHandler.List` — the handler iterates the fresh (or cached) run list and resets all phase gauges.

---

## End-to-end flow

The following trace shows the path from trigger fire to run completion for a simple two-step chain (`extract → load`):

```
┌─────────────────────────────────────────────────────────────────┐
│  Trigger fires (cron tick / annotation / HTTP POST)             │
│  └─ callback writes FireRequest to fireCh                       │
│  └─ drainFireChannel goroutine stores in pendingFires           │
│  └─ wakeupCh GenericEvent enqueues WeaveTrigger                 │
│                                                                 │
│  WeaveTriggerReconciler.Reconcile()                             │
│  └─ reads pendingFires for this trigger                         │
│  └─ checks concurrencyPolicy (allow / wait)                     │
│  └─ creates WeaveRun{chainRef, paramOverrides}                  │
│                                                                 │
│  WeaveRunReconciler.Reconcile()  [first call]                   │
│  └─ phase "" → set Running, requeue                             │
│                                                                 │
│  WeaveRunReconciler.Reconcile()  [second call]                  │
│  └─ BuildGraph([extract, load])                                 │
│  └─ Advance()  →  extract=Start, load=Wait                     │
│  └─ jobbuilder.Build(extract) → client.Create(Job)              │
│  └─ status.steps[extract] = Running                             │
│                                                                 │
│  Job "extract" completes in Kubernetes                          │
│  └─ Job watch handler enqueues WeaveRun                         │
│                                                                 │
│  WeaveRunReconciler.Reconcile()  [third call]                   │
│  └─ sync: extract Job = Complete → extract=Succeeded            │
│  └─ capture stdout → ConfigMap key "step-extract"               │
│  └─ prepareInputData(load) → ConfigMap key "input-load" ready   │
│  └─ Advance()  →  extract=Terminal, load=Start                  │
│  └─ jobbuilder.Build(load, inputCM) → client.Create(Job)        │
│  └─ status.steps[load] = Running                                │
│                                                                 │
│  Job "load" completes in Kubernetes                             │
│  └─ Job watch handler enqueues WeaveRun                         │
│                                                                 │
│  WeaveRunReconciler.Reconcile()  [fourth call]                  │
│  └─ sync: load Job = Complete → load=Succeeded                  │
│  └─ Advance()  →  all Terminal, RunComplete=true, Succeeded     │
│  └─ status.phase = Succeeded, completionTime = now              │
└─────────────────────────────────────────────────────────────────┘
```

Each reconcile cycle is **idempotent**: re-running it on the same state produces no change. The watch handlers ensure the reconciler is called as soon as something changes, without polling.

### Variant trace: a BatchCron-fired run with a stepOverrides deploy step

The DAG-advancement mechanics above are unchanged; only the trigger-fire and deploy-step-sync stages differ:

```
BatchCronScheduler goroutine's min-heap timer fires for job "ingest-daily"
  └─ BatchFireRequest{TriggerName, JobID, ParameterOverrides: JOB_ID/JOB_NAME/...} → batchFireCh
  └─ drainBatchFireChannel() stores in pendingBatchFires, wakes WeaveTrigger

WeaveTriggerReconciler.Reconcile()
  └─ consumePendingBatchFires() → maybeCreateBatchRun() → createBatchRun()
  └─ creates WeaveRun{chainRef, parameterOverrides: JOB_* env vars,
                       stepOverrides: [{stepName: "api", artifactName: "app.my-service", tag: "stable"}]}

WeaveRunReconciler.Reconcile()
  └─ Advance() → step "api" (stepKind: Deploy) = Start
  └─ findStepOverride(run.Spec.StepOverrides, "api") → found
  └─ syncDeployStepFromOverride():
       indexclient.FetchAppMetadataAndVersion(indexURL, "app.my-service", "stable")
       deploybuilder.BuildFromOverride(...) → Deployment "<runName>-api" (owner: WeaveRun)
  └─ status.steps[api] = Running, DeploymentRef = "<runName>-api"

  [later reconcile, once Deployment Available]
  └─ isDeploymentAvailable() = true → step "api" = Deployed (non-terminal)
  └─ registerRunActiveDeployment() → run.Status.ActiveDeployments["<runName>-api"] = {...}
  └─ RunComplete stays false while "api" is Deployed — run remains Running indefinitely

  [subsequent reconciles, every CodeSourcePollInterval]
  └─ pollRunDeploymentCodeSource(): ResolveTag(indexURL, artifact, tag) →
       if changed, patch restartedAt + WEAVE_VERSION on "<runName>-api" (rolling restart)
```

Unlike the chain-owned deploy path, this Deployment is never shared with another run — a second BatchCron fire creates a second WeaveRun with its own `<runName2>-api` Deployment — and it is deleted (not just detached) by `doDeployTeardown` when this specific run reaches a terminal phase or is deleted.
