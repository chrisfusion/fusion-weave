# fusion-weave

[![License: GPL-3.0](https://img.shields.io/badge/License-GPL--3.0-blue.svg)](LICENSE)

A Kubernetes operator that schedules configurable job DAGs. Define reusable job and service templates, compose them into dependency chains, and fire runs on-demand, on a cron schedule, from a batch job list, via HTTP webhook, or from Kafka/S3 events.

## Features

- **WeaveJobTemplate** — reusable batch job spec (image, command, resources, probes, security contexts, code-source artifact loading)
- **WeaveServiceTemplate** — reusable long-running deployment spec (rolling-update steps, Ingress, code-source artifact loading)
- **WeaveChain** — DAG of steps with dependency edges, shared storage, step output passing, and an optional shared auth secret injected into every step
- **WeaveTrigger** — fires runs on a cron schedule, on-demand annotation, HTTP webhook, a BatchCron job list (per-job schedules from a ConfigMap), or Kafka/S3-event messages
- **WeaveRun** — execution record tracking per-step status, output capture, phase, and optional per-run deploy-step overrides (`stepOverrides`)
- **Code-source artifact loading** — Job and Deploy steps can pull a versioned artifact from fusion-index at pod start via an init container, with polling-based rolling restarts on tag moves
- **Per-run service instances** — `WeaveRun.spec.stepOverrides` lets multiple runs of one WeaveChain each get their own Deployment for a deploy-kind step, without cloning the chain
- **REST API** — full CRUD over all five CRDs via a standalone HTTP service with API key, OIDC, and SA token authentication
- **Monitoring API** — read-only observability endpoints at `/monitor/v1/` for run status, batch jobs, pod logs, deployment health, aggregated stats, and Kubernetes Events; backed by an in-memory TTL cache
- **Prometheus metrics** — operator and API server metrics on a dedicated port (`:9091`), including per-phase run gauges and cache hit/miss counters
- **Log streaming sink** — pluggable log sink interface with a Kafka implementation; log snapshots are published asynchronously after each fetch
- **Backup/restore** — a daily CronJob (`cmd/backup`) dumps WeaveJobTemplate/WeaveServiceTemplate/WeaveChain/WeaveTrigger specs to S3 for disaster recovery; restore is manual-only
- **Helm-configurable security contexts** — cluster-wide pod/container `SecurityContext` defaults for every workload pod, overridable per WeaveJobTemplate/WeaveServiceTemplate
- **Showroom** — an optional, self-contained set of example WeaveChains covering most features, deployable with a single Helm flag

## Architecture

```
cmd/main.go          — operator entry point (controller-runtime manager)
cmd/api/main.go       — REST API server entry point (chi router)
cmd/backup/main.go    — backup/restore binary (dump/restore CRD specs to/from S3)
cmd/loader/main.go    — init container entry point for code-source artifact loading

api/v1alpha1/         — CRD type definitions and deepcopy
internal/
  controller/         — 5 reconcilers (one per CRD)
  dag/                — pure-Go DAG engine (no k8s dependency)
  jobbuilder/         — translates WeaveJobTemplate + WeaveChainStep + WeaveRun → batch/v1 Job
  deploybuilder/      — translates WeaveServiceTemplate (or a WeaveRun stepOverride) → apps/v1 Deployment + Service + Ingress
  codesource/         — fusion-index artifact resolution + name-truncation helpers shared by jobbuilder/deploybuilder
  trigger/            — cron scheduler, BatchCron scheduler, Kafka consumer, webhook HTTP server
  security/           — parses WORKLOAD_SECURITY_DEFAULTS into pod/container SecurityContext defaults
  backup/             — S3 backup/restore of CRD specs (pure Go, unit-tested via the controller-runtime fake client)
  apiserver/          — REST API (router, auth, middleware, handlers)
  monitoring/         — monitoring API (cache, logsink, handlers, Prometheus metrics server)
  indexclient/        — minimal HTTP client for fusion-index tag/version resolution

config/
  crd/bases/          — generated CRD manifests
  rbac/                — ServiceAccount, Role, RoleBinding for operator and API server
  manager/             — raw-YAML Deployment manifests for quick iteration
  samples/             — example CRD instances

deployment/
  fusion-weave/        — Helm chart (includes the showroom/ demo bundle)
```

## Prerequisites

- Go 1.25+
- Kubernetes 1.27+ cluster (or minikube)
- `kubectl` configured
- `helm` 3.x (for Helm-based deploy)
- `controller-gen` for regenerating CRD manifests: `go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest`

## Quick Start (minikube)

```bash
# Start minikube and point Docker at its daemon
minikube start
eval $(minikube docker-env)

# Build the image (produces /manager, /api-server, /loader, and /backup binaries)
docker build -t fusion-weave-operator:latest .

# Create namespace
kubectl create namespace fusion

# Install CRDs
kubectl apply -f config/crd/bases/

# Deploy operator + API server RBAC and workloads
kubectl apply -f config/rbac/
kubectl apply -f config/manager/

# Verify
kubectl get pods -n fusion
```

## Helm Install

```bash
helm upgrade --install fusion-weave deployment/fusion-weave/ \
  --set image.repository=fusion-weave-operator \
  --set image.tag=latest \
  --set image.pullPolicy=Never \
  --set namespace=fusion \
  --set namespaceCreate=false
```

To install the self-contained demo tour alongside it:

```bash
helm upgrade --install fusion-weave deployment/fusion-weave/ \
  ... \
  --set showroom.enabled=true \
  --set showroom.sharedStorage.storageClassName=csi-hostpath-sc
```

See [deployment/fusion-weave/README.md](deployment/fusion-weave/README.md) for all available values.

## Using the Monitoring API

Enable the monitoring API with `--monitoring` (or `MONITORING_ENABLED=true`). It shares the same port and auth model as the CRUD API.

```bash
kubectl port-forward svc/fusion-weave-api 8082:8082 -n fusion &

# Aggregated run statistics (last hour)
curl -H "Authorization: Bearer $KEY" http://localhost:8082/monitor/v1/stats/runs

# Stats for a specific chain over the last 24 h
curl -H "Authorization: Bearer $KEY" "http://localhost:8082/monitor/v1/stats/chains/etl-pipeline?window=24h"

# Full run status with jobs and events
curl -H "Authorization: Bearer $KEY" http://localhost:8082/monitor/v1/runs/etl-run-1

# Logs for a specific step (last 100 lines)
curl -H "Authorization: Bearer $KEY" http://localhost:8082/monitor/v1/runs/etl-run-1/steps/extract/logs

# All batch jobs for a run
curl -H "Authorization: Bearer $KEY" http://localhost:8082/monitor/v1/runs/etl-run-1/jobs

# Deployments owned by a chain
curl -H "Authorization: Bearer $KEY" http://localhost:8082/monitor/v1/chains/ci-demo/deployments

# Kubernetes events for a run
curl -H "Authorization: Bearer $KEY" http://localhost:8082/monitor/v1/runs/etl-run-1/events

# All events (optional field selector)
curl -H "Authorization: Bearer $KEY" \
  "http://localhost:8082/monitor/v1/events?fieldSelector=reason=Failed"
```

Responses are cached (default TTL: 30 s). Prometheus metrics are served separately on `:9091/metrics`.

## Using the REST API

The API server listens on port `8082` and exposes full CRUD for all five CRDs under `/api/v1/`.

```bash
# Port-forward during development
kubectl port-forward svc/fusion-weave-api 8082:8082 -n fusion &

# Health check
curl http://localhost:8082/healthz

# List all chains
curl http://localhost:8082/api/v1/chains

# Create a job template
curl -X POST http://localhost:8082/api/v1/jobtemplates \
  -H "Content-Type: application/json" \
  -d @config/samples/weavejobtemplate_echo.yaml

# Patch a resource
curl -X PATCH http://localhost:8082/api/v1/jobtemplates/my-template \
  -H "Content-Type: application/merge-patch+json" \
  -d '{"spec":{"image":"busybox:1.36"}}'

# Delete a resource
curl -X DELETE http://localhost:8082/api/v1/jobtemplates/my-template
```

### Authentication

The API server supports three auth modes (combinable):

| Mode | Enable flag | Identity source | Role source |
|---|---|---|---|
| API key | `AUTH_APIKEY=true` | Secret with label `fusion-platform.io/api-key=true` | Secret annotation `fusion-platform.io/role` |
| OIDC JWT | `AUTH_OIDC=true` | JWT sub claim | JWT claim named by `OIDC_ROLE_CLAIM` (default: `fusion-weave-role`) |
| SA token | `AUTH_SA=true` | ServiceAccount name | SA label `fusion-platform.io/role` (default: `viewer`) |

Roles: `viewer` (GET), `editor` (GET + POST + PUT + PATCH), `admin` (all including DELETE).

Set `ALLOW_UNAUTHENTICATED=true` for cluster-internal mode (grants admin — never use in production).

#### Creating an API key

```bash
# Generate a key
KEY=$(openssl rand -hex 32)

# Create the Secret
kubectl create secret generic my-api-key \
  --from-literal=key="$KEY" \
  --namespace=fusion
kubectl label secret my-api-key fusion-platform.io/api-key=true -n fusion
kubectl annotate secret my-api-key fusion-platform.io/role=editor -n fusion

# Use the key
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/chains
```

## Triggers

A WeaveTrigger fires runs of a WeaveChain. `spec.type` selects the activation mode:

| Type | Fires when | Config field |
|---|---|---|
| `OnDemand` | `fusion-platform.io/fire=true` annotation is set | — |
| `Cron` | a 6-field (seconds-first) cron schedule elapses | `spec.schedule` |
| `Webhook` | an HTTP POST hits the trigger's path | `spec.webhook` |
| `BatchCron` | any job in a ConfigMap-backed job list reaches its own 5-field cron schedule | `spec.batchCron` |
| `Kafka` | a message (optionally S3-event-shaped) arrives on a Kafka topic | `spec.kafka` |

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveTrigger
metadata:
  name: nightly-etl
spec:
  chainRef: {name: etl-pipeline}
  type: Cron
  schedule: "0 0 3 * * *"   # seconds-first: 3am daily
```

## Step Output Passing

Steps opt in to producing output with `producesOutput: true`. Downstream steps declare `consumesOutputFrom: [stepA]`. The operator captures JSON stdout from the producer and injects a merged JSON file at `/weave-input/input.json` in the consumer pod.

```yaml
steps:
  - name: extract
    jobTemplateRef: {name: extract-job}
    producesOutput: true
  - name: transform
    jobTemplateRef: {name: transform-job}
    consumesOutputFrom: [extract]
    dependsOn: [extract]
```

## Shared Storage

Opt in per chain with `spec.sharedStorage`. All job pods in the chain get `/weave-shared` mounted ReadWriteMany.

```yaml
spec:
  sharedStorage:
    size: "500Mi"
    storageClassName: "csi-hostpath-sc"   # must support RWX
```

On minikube: `minikube addons enable csi-hostpath-driver` (StorageClass: `csi-hostpath-sc`).

## Deploy Steps

Use `stepKind: Deploy` with a `serviceTemplateRef` to create/rolling-update a long-running Deployment alongside batch jobs in the same chain.

```yaml
steps:
  - name: deploy-api
    stepKind: Deploy
    serviceTemplateRef: {name: my-service-template}
```

The Deployment is owned by the WeaveChain (not the WeaveRun), so it persists across run deletions. The chain controller monitors health and auto-rollbacks after `spec.unhealthyDuration`.

If the WeaveServiceTemplate sets `spec.ingress`, each ingress rule's `name` is a DNS label only (never a full hostname) — the operator appends the cluster-wide `ingress.hostSuffix` Helm value to form the actual host: `<name>.<hostSuffix>`. This keeps a chain from ever pointing an Ingress at a domain the operator doesn't own. A template with `spec.ingress` set stays `status.valid=false` until `ingress.hostSuffix` is configured.

## Code-Source Artifacts

A WeaveJobTemplate or WeaveServiceTemplate can set `spec.codeSource` to pull a versioned artifact from [fusion-index](../fusion-index) into the pod at start via an init container — no baked-in application code required in the runner image.

```yaml
spec:
  codeSource:
    artifactName: "org.myteam.myapp"
    tag: "stable"          # mutable tag, resolved to a concrete version on every pod start
    mountPath: "/weave-code"
```

For Deploy steps, the chain controller polls fusion-index every `codeSource.pollInterval` (default `60s`) and rolling-restarts the Deployment when the tracked tag moves to a new version — or trigger it immediately with:

```bash
kubectl annotate weavechains <chain-name> \
  fusion-platform.io/reload-deploy-step=<stepName>@<version> --overwrite -n fusion
```

## Per-Run Service Instances (stepOverrides)

By default a deploy-kind step's Deployment is chain-owned and shared across every run. `WeaveRun.spec.stepOverrides` lets an individual run stand up its own instance instead — named `<runName>-<stepName>` and owned by the WeaveRun — reading runner configuration from the artifact's `metadata.yaml` in fusion-index rather than from the WeaveServiceTemplate:

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveRun
spec:
  chainRef: {name: my-chain}
  stepOverrides:
    - stepName: api
      artifactName: "org.myteam.myapp"
      tag: "pr-123"
      ingressName: "pr-123-api"   # DNS label; full host is <ingressName>.<ingress.hostSuffix>
```

## Backup and Restore

The `backup` binary (`cmd/backup`) dumps WeaveJobTemplate/WeaveServiceTemplate/WeaveChain/WeaveTrigger *specs* to S3 — never WeaveRun, never `.status`. A daily CronJob is templated in the Helm chart, gated by `backup.enabled`:

```bash
helm upgrade --install fusion-weave deployment/fusion-weave/ \
  ... \
  --set backup.enabled=true \
  --set backup.s3.bucket=my-backup-bucket
```

Restore is manual-only (not wired to any automatic Helm trigger) and refuses to run unless forced:

```bash
kubectl run fusion-weave-restore --rm -it --image=fusion-weave-operator:latest \
  --restart=Never -n fusion -- /backup restore
```

## Development

```bash
# Run unit tests
make test

# Regenerate CRD manifests + deepcopy after changing api/v1alpha1/
make generate
cp config/crd/bases/*.yaml deployment/fusion-weave/crds/

# Rebuild and redeploy on minikube
eval $(minikube docker-env) && docker build -t fusion-weave-operator:latest .
kubectl rollout restart deployment/fusion-weave-operator deployment/fusion-weave-api -n fusion
```

## CRD Short Names

| Short name | Resource |
|---|---|
| `fr` | WeaveRun |
| `ft` | WeaveTrigger |
| `fc` | WeaveChain |
| `fjt` | WeaveJobTemplate |
| `wst` | WeaveServiceTemplate |

## License

[GNU General Public License v3.0](LICENSE)
