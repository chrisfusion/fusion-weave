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
   - [WeaveRun reconciler — the execution engine](#weaverun-reconciler--the-execution-engine)
5. [DAG engine](#dag-engine)
6. [Job builder](#job-builder)
7. [Deploy builder](#deploy-builder)
8. [Step output passing](#step-output-passing)
9. [Shared storage](#shared-storage)
10. [Deploy steps and health monitoring](#deploy-steps-and-health-monitoring)
11. [REST API server](#rest-api-server)
12. [End-to-end flow](#end-to-end-flow)

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

---

## CRD data model

### WeaveJobTemplate

A reusable pod spec for a single batch job step. Stores image, command, args, env, resource requests/limits, and retry policy. Referenced by WeaveChain steps.

### WeaveServiceTemplate

A reusable spec for a long-running deployment step: image, replicas, ports, probes, service type, optional Ingress rules, `unhealthyDuration` (auto-rollback threshold), and `revisionHistoryLimit`. Referenced by chain steps with `stepKind: Deploy`.

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

### WeaveTrigger

An activation source for a WeaveChain. Three types:
- `Cron` — fires on a robfig/cron schedule (6-field, seconds-prefixed)
- `OnDemand` — fires when the annotation `fusion-platform.io/fire=true` is set
- `Webhook` — fires on an authenticated HTTP POST to a configured path

### WeaveRun

The mutable execution record. Created by the trigger reconciler; driven by the run reconciler. Stores the overall phase (`Running`, `Succeeded`, `Failed`), per-step status (phase, job ref, deployment ref, output captured, retry count), start/completion times, and the name of any shared PVC provisioned for this run.

---

## Repository layout

```
cmd/
  main.go            — operator entry point: builds the manager, registers all 5 reconcilers
  api/
    main.go          — REST API server entry point (separate binary)
    config.go        — flag/env var configuration

api/v1alpha1/        — CRD Go types + deepcopy generated code

internal/
  controller/        — 5 reconcilers
    weavejobtemplate_controller.go
    weaveservicetemplate_controller.go
    weavechain_controller.go
    weavetrigger_controller.go
    weaverun_controller.go     ← main execution engine
    deploy_helpers.go          — isDeploymentAvailable()

  dag/
    graph.go         — DAG construction + topological sort (Kahn's algorithm)
    executor.go      — pure Advance() function: no k8s dependency

  jobbuilder/
    builder.go       — WeaveJobTemplate → batch/v1 Job spec

  deploybuilder/
    builder.go       — WeaveServiceTemplate → apps/v1 Deployment + Service + Ingress
    names.go         — deterministic resource name helpers

  trigger/
    cron.go          — CronScheduler (wraps robfig/cron, upsert/remove by key)
    webhook.go       — WebhookServer (chi HTTP server, bearer token auth)

  apiserver/
    server.go        — Config, Server, Start()
    router.go        — chi router, health routes, /api/v1 sub-router
    types.go         — APIError exported type
    auth/            — APIKeyValidator, OIDCValidator, SAValidator, Authenticator
    middleware/       — Recovery, Logging, Auth (sync.Once), RBAC
    handlers/        — ResourceHandler interface + 5 CRD handler structs

config/
  crd/bases/         — generated CRD YAML (kubectl apply target)
  rbac/              — ServiceAccount, Role, RoleBinding for operator and API server
  manager/           — raw-YAML Deployments for quick iteration
  samples/           — example CRD instances

deployment/fusion-weave/   — Helm chart
```

---

## Operator internals

### Controller-runtime manager

`cmd/main.go` creates a single `controller-runtime` manager scoped to the `fusion` namespace (via `cache.Options{DefaultNamespaces: ...}`). The manager runs all reconcilers in the same process.

Shared components created before reconciler registration:

```
CronScheduler   ──────────┐
WebhookServer   ──────────┤──► fireCh (chan FireRequest)
                           │         │
                    WeaveTriggerReconciler ◄── drainFireChannel goroutine
```

`fireCh` is a buffered channel. The webhook and cron callbacks write to it from outside the reconcile loop; a dedicated goroutine in the trigger reconciler drains it and enqueues the reconciler via a `source.Channel` + `WatchesRawSource`. This is necessary because controller-runtime's reconcile loop is not re-entrant.

### WeaveJobTemplate / WeaveServiceTemplate reconcilers

Simple validation-only reconcilers. They parse the template spec, check invariants (image required, ports non-empty, resource quantities parseable, ingress rules valid), and write a `valid: true/false` condition to status. They do not create any Kubernetes workloads — that is the run reconciler's job.

### WeaveChain reconciler

Validates the chain at admission time:
- Calls `dag.BuildGraph` to detect cycles and missing dependency references
- For every step with `consumesOutputFrom`, verifies each named producer is an ancestor with `producesOutput: true`
- Validates `sharedStorage.size` is a parseable resource quantity

After initial validation the chain reconciler monitors the health of active deploy-kind steps registered in `chain.Status.ActiveDeployments`. If a deployment transitions to unhealthy and stays unhealthy past `unhealthyDuration`, the chain controller performs a rollback by fetching the previous ReplicaSet revision and patching `Deployment.Spec.Template` back to it.

The chain reconciler watches `apps/v1 Deployments` with a label-based handler: when a Deployment changes, the reconciler is enqueued for the WeaveChain identified by the `fusion-platform.io/chain` label.

### WeaveTrigger reconciler and the fire channel

The trigger reconciler manages the lifecycle of cron jobs and webhook routes:

```
Reconcile() called
  └── type = Cron    → CronScheduler.Upsert(key, schedule, callback)
  └── type = OnDemand → check annotation fusion-platform.io/fire=true
  └── type = Webhook  → WebhookServer.Register(path, token, callback)
```

When a cron tick or webhook POST fires the callback:
1. The callback writes a `FireRequest{TriggerName, TriggerNamespace, Overrides}` to `fireCh`
2. `drainFireChannel()` goroutine reads from `fireCh`, stores the request in `pendingFires` (keyed by `namespace/name`), and sends a `GenericEvent` to `wakeupCh`
3. The `source.Channel` watcher enqueues the trigger for reconciliation
4. On the next reconcile the trigger controller reads `pendingFires`, applies `concurrencyPolicy` (Allow / Wait), and creates a `WeaveRun` object

For **OnDemand** triggers, the annotation is detected directly in `Reconcile()` — no channel hop needed because the annotation change already triggers a reconcile via the standard watch.

### WeaveRun reconciler — the execution engine

The run reconciler is the most complex component. Its `Reconcile()` method is called every time a WeaveRun or a watched child resource (Job, Deployment) changes. Because it is idempotent and edge-triggered, it drives the DAG forward incrementally across many calls.

**Each reconcile cycle:**

```
1.  Get WeaveRun                          (return if terminal)
2.  Get WeaveChain
3.  Ensure shared PVC (if sharedStorage configured)
4.  Load all referenced job/service templates
5.  dag.BuildGraph(chain.Spec.Steps)
6.  Snapshot current step states from run.Status.Steps
7.  Sync running steps:
      Job steps    → check batch/v1 Job conditions (Complete/Failed)
      Deploy steps → check Deployment Available condition
      Capture stdout output for completed producing steps
8.  dag.Advance(graph, states, failurePolicy)
9.  For each DecisionStart:
      Job step    → jobbuilder.Build() → client.Create(Job)
      Deploy step → deploybuilder.Build() → client.Apply(Deployment+Service+Ingress)
10. Write updated status (steps, phase, completion time)
```

The reconciler watches `batch/v1 Jobs` and `apps/v1 Deployments` via label-based handlers: when a Job completes, the handler maps it back to the owning WeaveRun and enqueues it. This ensures the reconciler is woken up promptly without polling.

**Optimistic concurrency:** `client.MergeFrom(run.DeepCopy())` is captured immediately after `r.Get()`, before any mutations, so status patches always diff against the last-read version. If two concurrent reconciles race, the second will receive a conflict error and be requeued.

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
Currently running (Running/Retrying)         → DecisionWait
StopAll in effect (any step failed)          → DecisionSkip
Any dependency not yet terminal              → DecisionWait
conditionMet (runOnSuccess/runOnFailure)     → DecisionStart
Otherwise                                    → DecisionSkip
```

`conditionMet` checks `runOnSuccess` and `runOnFailure` against the terminal states of direct dependencies:
- `runOnSuccess=true` → start if ALL deps succeeded
- `runOnFailure=true` → start if ANY dep failed
- Both can be true simultaneously (step runs regardless of upstream outcome)

The overall run is **complete** when no step is in `DecisionWait`, and **succeeded** when complete and no step failed.

---

## Job builder

`internal/jobbuilder/builder.go` translates a `WeaveJobTemplate` + chain step + run into a complete `batch/v1 Job` spec.

Key naming conventions:
- Job name: `<runName>-<stepName>-<retryCount>` (deterministic, retry-safe)
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

---

## Deploy builder

`internal/deploybuilder/builder.go` translates a `WeaveServiceTemplate` into:
- `apps/v1 Deployment` with a `RollingUpdate` strategy
- `corev1.Service` of the configured type
- `networking.k8s.io/v1 Ingress` (only when `spec.ingress` is set)

Resource naming: `<chainName>-<stepName>` — **stable across runs**. This is what enables rolling updates: the same Deployment is patched in place on every run rather than a new one being created.

**Immutable selector labels:**
```
fusion-platform.io/chain = <chainName>
fusion-platform.io/step  = <stepName>
```

The run name is deliberately excluded from selector labels. Kubernetes forbids changing `spec.selector` after creation; including the run name would break every subsequent run.

**Owner reference:** The Deployment is owned by the **WeaveChain** (not the WeaveRun). This means the Deployment survives run deletion and continues serving traffic. Only deleting the WeaveChain (or the chain step) garbage-collects the Deployment.

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

1. `deploybuilder.Build()` constructs the Deployment, Service, and optional Ingress specs
2. The reconciler calls `client.Apply` (server-side apply) to create or rolling-update the resources
3. Owner reference set to **WeaveChain** (stable, survives runs)
4. The step is marked `Running` with `DeploymentRef` pointing to the Deployment name

On subsequent reconcile cycles the run reconciler checks `isDeploymentAvailable()`, which looks for `DeploymentAvailable condition = True` in the Deployment status. When available:
- Step phase → `Succeeded`
- `registerActiveDeployment()` patches `WeaveChain.Status.ActiveDeployments` to register this step for ongoing health monitoring

**Post-run health monitoring (WeaveChain reconciler):**

`syncDeploymentHealth()` runs on every chain reconcile and iterates `ActiveDeployments`:

```
Available=True  → phase = Healthy (or stays Healthy)
Available=False → phase = Unhealthy, record timestamp
Unhealthy for > unhealthyDuration → phase = RollingBack
  → rollbackDeployment(): fetch all ReplicaSets by label,
    find revision N-1, patch Deployment.Spec.Template back to it
After rollback → phase = RolledBack
```

The chain reconciler watches Deployments via a label-based handler (`fusion-platform.io/chain`) so it is notified immediately when a Deployment's availability changes.

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

`PATCH` uses **JSON Merge Patch** (`application/merge-patch+json`). The handler fetches the current resource (populating `resourceVersion` for optimistic concurrency), then calls `client.Patch` with the raw merge-patch bytes. The API server applies the patch and returns the updated object.

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
