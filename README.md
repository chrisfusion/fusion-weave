# fusion-weave

[![License: GPL-3.0](https://img.shields.io/badge/License-GPL--3.0-blue.svg)](LICENSE)

A Kubernetes operator that schedules configurable job DAGs. Define reusable job templates, compose them into dependency chains, and fire runs on-demand, on a cron schedule, or via HTTP webhook.

## Features

- **WeaveJobTemplate** — reusable batch job spec (image, command, resources, probes)
- **WeaveServiceTemplate** — reusable long-running deployment spec (rolling-update steps)
- **WeaveChain** — DAG of steps with dependency edges, shared storage, and step output passing
- **WeaveTrigger** — fires runs on a cron schedule, on-demand annotation, or HTTP webhook
- **WeaveRun** — execution record tracking per-step status, output capture, and phase
- **REST API** — full CRUD over all five CRDs via a standalone HTTP service with API key, OIDC, and SA token authentication

## Architecture

```
cmd/main.go          — operator entry point (controller-runtime manager)
cmd/api/main.go      — REST API server entry point (chi router)

api/v1alpha1/        — CRD type definitions and deepcopy
internal/
  controller/        — 5 reconcilers (one per CRD)
  dag/               — pure-Go DAG engine (no k8s dependency)
  jobbuilder/        — translates WeaveJobTemplate → batch/v1 Job
  deploybuilder/     — translates WeaveServiceTemplate → apps/v1 Deployment + Service + Ingress
  trigger/           — cron scheduler and webhook HTTP server
  apiserver/         — REST API (router, auth, middleware, handlers)

config/
  crd/bases/         — generated CRD manifests
  rbac/              — ServiceAccount, Role, RoleBinding for operator and API server
  manager/           — raw-YAML Deployment manifests for quick iteration
  samples/           — example CRD instances

deployment/
  fusion-weave/      — Helm chart
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

# Build the image (produces both /manager and /api-server binaries)
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

See [deployment/fusion-weave/README.md](deployment/fusion-weave/README.md) for all available values.

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
