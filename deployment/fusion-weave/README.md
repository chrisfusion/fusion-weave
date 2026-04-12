# fusion-weave Helm Chart

Deploys the fusion-weave operator and REST API server into a Kubernetes namespace.

## Installing

```bash
helm upgrade --install fusion-weave deployment/fusion-weave/ \
  --set image.repository=ghcr.io/chrisfusion/fusion-weave-operator \
  --set image.tag=1.0.0 \
  --set namespace=fusion \
  --set namespaceCreate=true
```

### minikube (local image, no pull)

```bash
eval $(minikube docker-env)
docker build -t fusion-weave-operator:latest ../../.
helm upgrade --install fusion-weave . \
  --set image.repository=fusion-weave-operator \
  --set image.tag=latest \
  --set image.pullPolicy=Never \
  --set namespace=fusion \
  --set namespaceCreate=false
```

### With SA token auth + sample chain

```bash
helm upgrade --install fusion-weave . \
  --set image.repository=fusion-weave-operator \
  --set image.tag=latest \
  --set image.pullPolicy=Never \
  --set namespace=fusion \
  --set namespaceCreate=false \
  --set api.auth.saAuthEnabled=true \
  --set samples.enabled=true \
  --set samples.sharedStorage.storageClassName=csi-hostpath-sc
```

## Uninstalling

```bash
helm uninstall fusion-weave
```

> CRDs are **not** deleted on uninstall (Helm behaviour for `crds/` directory). Remove manually if needed:
> ```bash
> kubectl delete crd \
>   weavejobtemplates.weave.fusion-platform.io \
>   weaveservicetemplates.weave.fusion-platform.io \
>   weavechains.weave.fusion-platform.io \
>   weavetriggers.weave.fusion-platform.io \
>   weaveruns.weave.fusion-platform.io
> ```

## Values

### Core

| Key | Default | Description |
|---|---|---|
| `namespace` | `fusion` | Kubernetes namespace the operator manages. All CRD instances must live here. |
| `namespaceCreate` | `true` | Create the namespace as part of the release. Set `false` when the namespace already exists to avoid Helm ownership conflicts. |

### Operator image

| Key | Default | Description |
|---|---|---|
| `image.repository` | `ghcr.io/chrisfusion/fusion-weave-operator` | Container image repository for the operator. Both `/manager` and `/api-server` binaries are in this image. |
| `image.tag` | `latest` | Image tag. |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy. Use `Never` for local minikube builds. |

### Operator deployment

| Key | Default | Description |
|---|---|---|
| `replicaCount` | `1` | Number of operator replicas. Set `>1` only together with `leaderElection.enabled=true`. |
| `leaderElection.enabled` | `false` | Enable leader election for HA. Required when `replicaCount > 1`. |
| `serviceAccount.name` | `fusion-weave-operator` | Name of the operator ServiceAccount and its Role/RoleBinding. |
| `resources.requests.cpu` | `50m` | CPU request for the operator pod. |
| `resources.requests.memory` | `64Mi` | Memory request for the operator pod. |
| `resources.limits.cpu` | `500m` | CPU limit for the operator pod. |
| `resources.limits.memory` | `128Mi` | Memory limit for the operator pod. |

### Operator ports

| Key | Default | Description |
|---|---|---|
| `ports.metrics` | `8080` | Prometheus metrics endpoint port. |
| `ports.health` | `8081` | Liveness/readiness probe port. |
| `ports.webhook` | `9090` | Webhook trigger HTTP server port. |

### Webhook service

| Key | Default | Description |
|---|---|---|
| `webhookService.enabled` | `true` | Expose the webhook trigger port as a Kubernetes Service. |
| `webhookService.type` | `ClusterIP` | Service type (`ClusterIP`, `NodePort`, `LoadBalancer`). |
| `webhookService.port` | `9090` | Service port. |

### REST API server

| Key | Default | Description |
|---|---|---|
| `api.enabled` | `true` | Deploy the REST API server. Set `false` to skip entirely. |
| `api.image.repository` | `""` | Override image repository for the API server. Empty inherits `image.repository`. |
| `api.image.tag` | `""` | Override image tag for the API server. Empty inherits `image.tag`. |
| `api.image.pullPolicy` | `""` | Override pull policy for the API server. Empty inherits `image.pullPolicy`. |
| `api.serviceAccount.name` | `fusion-weave-api` | ServiceAccount name for the API server. |
| `api.replicaCount` | `1` | Number of API server replicas. |
| `api.resources.requests.cpu` | `50m` | CPU request for the API server pod. |
| `api.resources.requests.memory` | `64Mi` | Memory request for the API server pod. |
| `api.resources.limits.cpu` | `500m` | CPU limit for the API server pod. |
| `api.resources.limits.memory` | `128Mi` | Memory limit for the API server pod. |
| `api.service.type` | `ClusterIP` | API server Service type. |
| `api.service.port` | `8082` | API server Service port. |

### REST API authentication

All auth modes are disabled by default. Enable one or more as needed. Unauthenticated requests receive `401` when any auth mode is active.

| Key | Default | Description |
|---|---|---|
| `api.auth.apiKeyEnabled` | `false` | Enable API key auth. Keys are Kubernetes Secrets in the managed namespace with label `fusion-platform.io/api-key=true`. Role is read from annotation `fusion-platform.io/role` (`viewer`/`editor`/`admin`). |
| `api.auth.oidcEnabled` | `false` | Enable OIDC JWT auth. The server performs JWKS discovery against `oidcIssuerURL` on first request. |
| `api.auth.oidcIssuerURL` | `""` | OIDC provider issuer URL (e.g. `https://accounts.google.com`). Required when `oidcEnabled=true`. |
| `api.auth.oidcClientID` | `""` | Expected JWT audience / client ID. Required when `oidcEnabled=true`. |
| `api.auth.oidcRoleClaim` | `fusion-weave-role` | JWT claim name carrying the role (`viewer`/`editor`/`admin`). |
| `api.auth.saAuthEnabled` | `false` | Enable Kubernetes ServiceAccount token auth via TokenReview API. Also creates a ClusterRole + ClusterRoleBinding for `authentication.k8s.io/tokenreviews`. Role is read from SA label `fusion-platform.io/role` (defaults to `viewer`). |
| `api.auth.allowUnauthenticated` | `false` | Disable all auth checks — every caller receives `admin` access. Intended for cluster-internal mode only. **Never enable in production.** |

### Roles

| Role | Allowed HTTP methods |
|---|---|
| `viewer` | GET |
| `editor` | GET, POST, PUT, PATCH |
| `admin` | GET, POST, PUT, PATCH, DELETE |

### Samples

| Key | Default | Description |
|---|---|---|
| `samples.enabled` | `false` | Deploy sample WeaveJobTemplate, WeaveChain, and WeaveTrigger resources. Provides a working shared-storage demo chain. |
| `samples.sharedStorage.size` | `500Mi` | PVC size for the demo chain's shared storage volume. |
| `samples.sharedStorage.storageClassName` | `""` | StorageClass to use (must support ReadWriteMany). On minikube: `csi-hostpath-sc` (requires `minikube addons enable csi-hostpath-driver`). Leave empty to use the cluster default. |

## Creating an API Key

```bash
KEY=$(openssl rand -hex 32)
kubectl create secret generic my-api-key \
  --from-literal=key="$KEY" \
  --namespace=fusion
kubectl label secret my-api-key fusion-platform.io/api-key=true -n fusion
kubectl annotate secret my-api-key fusion-platform.io/role=editor -n fusion

# Use the key
curl -H "Authorization: Bearer $KEY" http://<api-service>/api/v1/chains
```

## Accessing the API

```bash
# Port-forward for local access
kubectl port-forward svc/fusion-weave-api 8082:8082 -n fusion

curl http://localhost:8082/healthz
curl http://localhost:8082/api/v1/chains
curl http://localhost:8082/api/v1/runs
```

## Resources Deployed

| Resource | Name | Notes |
|---|---|---|
| Namespace | `<namespace>` | Only when `namespaceCreate=true` |
| ServiceAccount | `fusion-weave-operator` | Operator identity |
| Role | `fusion-weave-operator` | Full CRUD on all CRDs + batch jobs + PVCs + deployments |
| RoleBinding | `fusion-weave-operator` | Binds role to SA |
| Deployment | `<release>-operator` | The operator |
| Service | `<release>-webhook` | Exposes webhook port (when `webhookService.enabled=true`) |
| ServiceAccount | `fusion-weave-api` | API server identity (when `api.enabled=true`) |
| Role | `fusion-weave-api` | CRD CRUD + Secret list (when `api.enabled=true`) |
| RoleBinding | `fusion-weave-api` | Binds role to SA (when `api.enabled=true`) |
| ClusterRole | `<release>-api-tokenreview` | TokenReview permission (when `api.auth.saAuthEnabled=true`) |
| ClusterRoleBinding | `<release>-api-tokenreview` | Binds ClusterRole to SA (when `api.auth.saAuthEnabled=true`) |
| Deployment | `<release>-api` | The REST API server (when `api.enabled=true`) |
| Service | `<release>-api` | Exposes API port (when `api.enabled=true`) |
