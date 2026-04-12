# Installation Guide

This guide covers every installation path: local development on minikube, raw-YAML cluster deploy, and Helm production install.

---

## Prerequisites

| Tool | Minimum version | Notes |
|---|---|---|
| Go | 1.25 | Only needed to build from source |
| kubectl | 1.27 | Must be configured against your target cluster |
| Helm | 3.10 | Only needed for Helm-based install |
| Docker | 24 | Only needed to build the image |
| minikube | 1.32 | Only needed for local development |

---

## Option A — minikube (local development)

### 1. Start minikube

```bash
minikube start --cpus=4 --memory=4g
```

### 2. Point Docker at minikube's daemon

```bash
eval $(minikube docker-env)
```

> Run this in every new terminal session before building. The flag persists only for the current shell.

### 3. Build the image

```bash
docker build -t fusion-weave-operator:latest .
```

Both binaries (`/manager` and `/api-server`) are built into the same image.

### 4. Create the namespace

```bash
kubectl create namespace fusion
```

### 5. Install CRDs

```bash
kubectl apply -f config/crd/bases/
```

### 6. Deploy RBAC + operator + API server

```bash
# Operator RBAC
kubectl apply -f config/rbac/serviceaccount.yaml \
              -f config/rbac/role.yaml \
              -f config/rbac/rolebinding.yaml

# API server RBAC (includes ClusterRole for TokenReview)
kubectl apply -f config/rbac/api-serviceaccount.yaml \
              -f config/rbac/api-role.yaml \
              -f config/rbac/api-rolebinding.yaml \
              -f config/rbac/api-clusterrole.yaml \
              -f config/rbac/api-clusterrolebinding.yaml

# Workloads
kubectl apply -f config/manager/manager.yaml
kubectl apply -f config/manager/api-server.yaml
```

### 7. Verify

```bash
kubectl get pods -n fusion
# Expected output:
# fusion-weave-operator-xxxxx   1/1   Running   0   30s
# fusion-weave-api-xxxxx        1/1   Running   0   30s

kubectl get crd | grep weave
# weavejobtemplates.weave.fusion-platform.io
# weaveservicetemplates.weave.fusion-platform.io
# weavechains.weave.fusion-platform.io
# weavetriggers.weave.fusion-platform.io
# weaveruns.weave.fusion-platform.io
```

### 8. Access the REST API

```bash
kubectl port-forward svc/fusion-weave-api 8082:8082 -n fusion &
curl http://localhost:8082/healthz
# {"status":"ok"}
```

---

## Option B — Helm (recommended for staging/production)

### 1. Install CRDs

CRDs are bundled in the chart's `crds/` directory and installed automatically before any other resource.

```bash
helm upgrade --install fusion-weave deployment/fusion-weave/ \
  --set image.repository=ghcr.io/chrisfusion/fusion-weave-operator \
  --set image.tag=1.0.0 \
  --set namespace=fusion \
  --set namespaceCreate=true
```

### Common flag combinations

**Minimal — operator only, no API server:**
```bash
helm upgrade --install fusion-weave deployment/fusion-weave/ \
  --set image.repository=ghcr.io/chrisfusion/fusion-weave-operator \
  --set image.tag=1.0.0 \
  --set namespace=fusion \
  --set namespaceCreate=true \
  --set api.enabled=false
```

**With API key auth:**
```bash
helm upgrade --install fusion-weave deployment/fusion-weave/ \
  --set image.repository=ghcr.io/chrisfusion/fusion-weave-operator \
  --set image.tag=1.0.0 \
  --set namespace=fusion \
  --set namespaceCreate=true \
  --set api.auth.apiKeyEnabled=true
```

**With OIDC auth:**
```bash
helm upgrade --install fusion-weave deployment/fusion-weave/ \
  --set image.repository=ghcr.io/chrisfusion/fusion-weave-operator \
  --set image.tag=1.0.0 \
  --set namespace=fusion \
  --set namespaceCreate=true \
  --set api.auth.oidcEnabled=true \
  --set api.auth.oidcIssuerURL=https://accounts.google.com \
  --set api.auth.oidcClientID=my-client-id
```

**With SA token auth (creates ClusterRole for TokenReview):**
```bash
helm upgrade --install fusion-weave deployment/fusion-weave/ \
  --set image.repository=ghcr.io/chrisfusion/fusion-weave-operator \
  --set image.tag=1.0.0 \
  --set namespace=fusion \
  --set namespaceCreate=true \
  --set api.auth.saAuthEnabled=true
```

**With sample chains (shared-storage demo):**
```bash
# First enable the RWX storage addon on minikube:
minikube addons enable csi-hostpath-driver

helm upgrade --install fusion-weave deployment/fusion-weave/ \
  --set image.repository=fusion-weave-operator \
  --set image.tag=latest \
  --set image.pullPolicy=Never \
  --set namespace=fusion \
  --set namespaceCreate=false \
  --set samples.enabled=true \
  --set samples.sharedStorage.storageClassName=csi-hostpath-sc
```

### Using a values file (recommended for non-trivial configs)

```bash
# Copy and edit the default values
cp deployment/fusion-weave/values.yaml my-values.yaml
# Edit my-values.yaml...

helm upgrade --install fusion-weave deployment/fusion-weave/ -f my-values.yaml
```

### Verify Helm install

```bash
helm status fusion-weave
kubectl get pods -n fusion
kubectl get crd | grep weave
```

---

## Setting up API Authentication

### API key

```bash
# Generate a random key
KEY=$(openssl rand -hex 32)

# Create the secret
kubectl create secret generic my-api-key \
  --from-literal=key="$KEY" \
  --namespace=fusion

# Label and annotate to activate it
kubectl label   secret my-api-key fusion-platform.io/api-key=true  -n fusion
kubectl annotate secret my-api-key fusion-platform.io/role=editor   -n fusion

# Use the key
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/chains
```

Valid roles: `viewer` (GET only), `editor` (GET/POST/PUT/PATCH), `admin` (all including DELETE).

### ServiceAccount token

Label the ServiceAccount that will call the API:

```bash
kubectl label serviceaccount my-sa fusion-platform.io/role=editor -n fusion
```

Then pass the SA's mounted token as a bearer token. The operator validates it via the TokenReview API.

---

## Enabling optional features

### Shared storage (ReadWriteMany PVC per run)

Requires a StorageClass that supports ReadWriteMany. On minikube:

```bash
minikube addons enable csi-hostpath-driver
# StorageClass name: csi-hostpath-sc
```

On a real cluster use your NFS, CephFS, or EFS StorageClass.

Add `spec.sharedStorage` to a WeaveChain — see [EXAMPLES.md](EXAMPLES.md#shared-storage).

### Webhook triggers

The operator exposes an HTTP server on port `9090` for webhook-based firing. Create a `WeaveTrigger` with `type: Webhook` and a bearer token secret — see [EXAMPLES.md](EXAMPLES.md#webhook-trigger).

---

## Enabling the Monitoring API

The monitoring API is disabled by default. Enable it via Helm or by setting environment variables in the API server Deployment.

### Helm (recommended)

```bash
helm upgrade fusion-weave deployment/fusion-weave/ \
  --reuse-values \
  --set api.monitoring.enabled=true \
  --set api.monitoring.metricsPort=9091 \
  --set api.monitoring.cacheTTL=30s \
  --set api.monitoring.maxLogLines=100
```

With Kafka log streaming:

```bash
helm upgrade fusion-weave deployment/fusion-weave/ \
  --reuse-values \
  --set api.monitoring.enabled=true \
  --set api.monitoring.kafka.brokers=kafka.fusion.svc.cluster.local:9092 \
  --set api.monitoring.kafka.topic=weave-logs
```

### Raw-YAML (environment variables)

Edit `config/manager/api-server.yaml` and add:

```yaml
env:
  - name: MONITORING_ENABLED
    value: "true"
  - name: METRICS_ADDR
    value: ":9091"
  - name: MONITOR_CACHE_TTL
    value: "30s"
  - name: MONITOR_LOG_LINES
    value: "100"
  # Optional Kafka sink:
  - name: KAFKA_ENABLED
    value: "true"
  - name: KAFKA_BROKERS
    value: "kafka:9092"
  - name: KAFKA_TOPIC
    value: "weave-logs"
```

Then:
```bash
kubectl apply -f config/manager/api-server.yaml
kubectl rollout restart deployment/fusion-weave-api -n fusion
```

### Verify

```bash
# Monitoring routes
curl -H "Authorization: Bearer $KEY" http://localhost:8082/monitor/v1/stats/runs

# Prometheus metrics
kubectl port-forward svc/fusion-weave-api 9091:9091 -n fusion &
curl http://localhost:9091/metrics | grep weave_
```

### Monitoring API endpoints

| Path | Description |
|---|---|
| `GET /monitor/v1/runs` | All WeaveRun summaries |
| `GET /monitor/v1/runs/{name}` | Run detail with jobs and events |
| `GET /monitor/v1/runs/{name}/jobs` | batch/v1 Jobs for a run |
| `GET /monitor/v1/runs/{name}/jobs/{jobName}` | Single job |
| `GET /monitor/v1/runs/{name}/steps/{step}/logs` | Pod log snapshot |
| `GET /monitor/v1/runs/{name}/events` | Kubernetes events for a run |
| `GET /monitor/v1/events` | All events (optional `?fieldSelector=`) |
| `GET /monitor/v1/chains/{name}/deployments` | Deployments owned by a chain |
| `GET /monitor/v1/stats/runs` | Aggregated run stats (`?window=1h\|24h\|7d`) |
| `GET /monitor/v1/stats/chains/{name}` | Per-chain stats |

All monitoring endpoints require `viewer` role or higher.

---

## Upgrading

### Rebuild and redeploy (minikube)

```bash
eval $(minikube docker-env)
docker build -t fusion-weave-operator:latest .
kubectl rollout restart deployment/fusion-weave-operator deployment/fusion-weave-api -n fusion
kubectl rollout status  deployment/fusion-weave-operator deployment/fusion-weave-api -n fusion
```

### Upgrade CRD schemas after type changes

```bash
make generate
kubectl apply -f config/crd/bases/
# Update Helm chart CRD copies too:
cp config/crd/bases/*.yaml deployment/fusion-weave/crds/
```

> Helm never updates or deletes CRDs on `helm upgrade`. Always apply CRD changes with `kubectl apply` directly.

### Helm upgrade

```bash
helm upgrade fusion-weave deployment/fusion-weave/ -f my-values.yaml
```

---

## Uninstalling

### Raw YAML

```bash
kubectl delete -f config/manager/
kubectl delete -f config/rbac/
kubectl delete -f config/crd/bases/
kubectl delete namespace fusion
```

### Helm

```bash
helm uninstall fusion-weave
# CRDs are NOT removed by helm uninstall — delete manually if needed:
kubectl delete crd \
  weavejobtemplates.weave.fusion-platform.io \
  weaveservicetemplates.weave.fusion-platform.io \
  weavechains.weave.fusion-platform.io \
  weavetriggers.weave.fusion-platform.io \
  weaveruns.weave.fusion-platform.io
kubectl delete namespace fusion
```

---

## Troubleshooting

**Operator pod in `CrashLoopBackOff`**
```bash
kubectl logs deployment/fusion-weave-operator -n fusion
# Common cause: CRDs not installed before the operator started.
# Fix: kubectl apply -f config/crd/bases/ && kubectl rollout restart deployment/fusion-weave-operator -n fusion
```

**API server returns 401 on all requests**
```bash
# Check if AllowUnauthenticated is disabled and no auth mode is configured.
# For dev, set ALLOW_UNAUTHENTICATED=true in config/manager/api-server.yaml.
kubectl edit deployment fusion-weave-api -n fusion
```

**`kubectl get fc` ambiguous / conflicts with another CRD**
```bash
# Check for stale CRDs from old installs:
kubectl get crd | grep -i flux
# Delete any stale CRDs from previous fusion-flux installs.
```

**Shared storage PVC stuck in `Pending`**
```bash
kubectl get pvc -n fusion
# Cause: StorageClass does not support ReadWriteMany.
# Fix: minikube addons enable csi-hostpath-driver
# Then use storageClassName: csi-hostpath-sc in your WeaveChain.
```

**WeaveRun stuck in `Running` with no jobs appearing**
```bash
kubectl describe weaverun <name> -n fusion
kubectl logs deployment/fusion-weave-operator -n fusion | grep -i error
# Common cause: operator lacks RBAC to create batch/v1 Jobs.
# Fix: re-apply config/rbac/role.yaml
```

**Monitoring API returns 404 on `/monitor/v1/`**
```bash
# Check that MONITORING_ENABLED is set to "true" in the API server pod.
kubectl get deployment fusion-weave-api -n fusion -o jsonpath='{.spec.template.spec.containers[0].env}' | jq .
# Fix: helm upgrade with --set api.monitoring.enabled=true, then rollout restart.
```

**Prometheus port not reachable**
```bash
# Confirm METRICS_ADDR is set (default :9091) and the metrics containerPort is exposed.
kubectl get svc fusion-weave-api -n fusion -o yaml | grep -A3 ports
# The metrics port is only added to the Service when api.monitoring.enabled=true.
```

**Log snapshot returns empty lines**
```bash
# The container in the job pod must be named "job" (set by jobbuilder — do not override).
# Check with: kubectl get pod <pod-name> -n fusion -o jsonpath='{.spec.containers[*].name}'
# Also verify the API server RBAC includes pods/log: re-apply config/rbac/api-role.yaml
```
