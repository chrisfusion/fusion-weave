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

### With SA token auth + showroom (Tier 1)

```bash
helm upgrade --install fusion-weave . \
  --set image.repository=fusion-weave-operator \
  --set image.tag=latest \
  --set image.pullPolicy=Never \
  --set namespace=fusion \
  --set namespaceCreate=false \
  --set api.auth.saAuthEnabled=true \
  --set showroom.enabled=true \
  --set showroom.sharedStorage.storageClassName=csi-hostpath-sc
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
| `image.tag` | `0.2.1` | Image tag. |
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

### Operator pod metadata & security

| Key | Default | Description |
|---|---|---|
| `podAnnotations` | `{}` | Extra annotations added to the operator pod template. Useful for tooling (e.g. Datadog APM, Vault agent) or custom admission webhooks. |
| `podLabels` | `{}` | Extra labels added to the operator pod template. Useful for NetworkPolicies, PodDisruptionBudgets, or tag-based monitoring. |
| `podSecurityContext.runAsNonRoot` | `true` | Pod-level security context for the operator pod. Any `corev1.PodSecurityContext` field can be set. |
| `podSecurityContext.runAsUser` | `65532` | |
| `podSecurityContext.fsGroup` | `65532` | |
| `containerSecurityContext.allowPrivilegeEscalation` | `false` | Container-level security context for the operator container. Any `corev1.SecurityContext` field can be set. |
| `containerSecurityContext.capabilities.drop` | `["ALL"]` | |
| `containerSecurityContext.readOnlyRootFilesystem` | `true` | |

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
| `api.podAnnotations` | `{}` | Extra annotations added to the API server pod template. |
| `api.podLabels` | `{}` | Extra labels added to the API server pod template. |
| `api.podSecurityContext.runAsNonRoot` | `true` | Pod-level security context for the API server pod. Any `corev1.PodSecurityContext` field can be set. |
| `api.podSecurityContext.runAsUser` | `65532` | |
| `api.podSecurityContext.fsGroup` | `65532` | |
| `api.containerSecurityContext.allowPrivilegeEscalation` | `false` | Container-level security context for the API server container. Any `corev1.SecurityContext` field can be set. |
| `api.containerSecurityContext.capabilities.drop` | `["ALL"]` | |
| `api.containerSecurityContext.readOnlyRootFilesystem` | `true` | |
| `api.log.level` | `info` | Minimum log level for the API HTTP layer (`log/slog`): `debug`\|`info`\|`warn`\|`error`. |
| `api.log.format` | `json` | Log output format: `json`\|`text`. `json` is the default for Kubernetes log collectors. |

### REST API authentication

`saAuthEnabled` is enabled by default; all other auth modes are disabled by default. Enable additional modes as needed. Unauthenticated requests receive `401` when any auth mode is active.

| Key | Default | Description |
|---|---|---|
| `api.auth.apiKeyEnabled` | `false` | Enable API key auth. Keys are Kubernetes Secrets in the managed namespace with label `fusion-platform.io/api-key=true`. Role is read from annotation `fusion-platform.io/role` (`viewer`/`editor`/`admin`). |
| `api.auth.oidcEnabled` | `false` | Enable OIDC JWT auth. The server performs JWKS discovery against `oidcIssuerURL` on first request. |
| `api.auth.oidcIssuerURL` | `""` | OIDC provider issuer URL (e.g. `https://accounts.google.com`). Required when `oidcEnabled=true`. |
| `api.auth.oidcClientID` | `""` | Expected JWT audience / client ID. Required when `oidcEnabled=true`. |
| `api.auth.oidcRoleClaim` | `fusion-weave-role` | JWT claim name carrying the role (`viewer`/`editor`/`admin`). |
| `api.auth.saAuthEnabled` | `true` | Enable Kubernetes ServiceAccount token auth via TokenReview API. Also creates a ClusterRole + ClusterRoleBinding for `authentication.k8s.io/tokenreviews`. Role is read from SA label `fusion-platform.io/role` (defaults to `viewer`). |
| `api.auth.allowUnauthenticated` | `false` | Disable all auth checks — every caller receives `admin` access. Intended for cluster-internal mode only. **Never enable in production.** |

### Roles

| Role | Allowed HTTP methods |
|---|---|
| `viewer` | GET |
| `editor` | GET, POST, PUT, PATCH |
| `admin` | GET, POST, PUT, PATCH, DELETE |

### Monitoring API

| Key | Default | Description |
|---|---|---|
| `api.monitoring.enabled` | `false` | Mount `/monitor/v1/` routes on the API server and expose the metrics port. |
| `api.monitoring.metricsPort` | `9091` | Port for the Prometheus `/metrics` endpoint (separate from the REST API port). |
| `api.monitoring.cacheTTL` | `30s` | TTL for all monitoring response caches. Accepts Go duration strings (`30s`, `2m`). |
| `api.monitoring.maxLogLines` | `100` | Maximum number of log lines returned per step log request. |
| `api.monitoring.kafka.brokers` | `""` | Kafka broker address(es). Empty disables Kafka; logs are discarded after serving. |
| `api.monitoring.kafka.topic` | `weave-logs` | Kafka topic for log snapshot messages. |

Enable with API key auth and Prometheus scraping:

```bash
helm upgrade fusion-weave deployment/fusion-weave/ \
  --reuse-values \
  --set api.monitoring.enabled=true \
  --set api.monitoring.metricsPort=9091 \
  --set api.auth.apiKeyEnabled=true
```

### Workload pod security defaults

Applied to every pod the operator creates at runtime — both batch Job pods (job steps)
and Deployment pods (deploy steps). Separate from the operator/API server pod
configuration above. Serialized to JSON and passed to the operator via the
`WORKLOAD_SECURITY_DEFAULTS` env var; a WeaveJobTemplate/WeaveServiceTemplate can
override `podSecurityContext`/`containerSecurityContext` per-template.

| Key | Default | Description |
|---|---|---|
| `workload.security.podAnnotations` | `{}` | Extra annotations merged into every workload pod template. Use for Seccomp/AppArmor annotations, Istio injection, Datadog APM, etc. |
| `workload.security.podLabels` | `{}` | Extra labels merged into every workload pod template. Use for NetworkPolicies, PodDisruptionBudgets, monitoring tag selectors. |
| `workload.security.podSecurityContext.runAsNonRoot` | `true` | Pod-level security context for every workload pod. Any `corev1.PodSecurityContext` field can be set (e.g. `seccompProfile`, `runAsUser`). |
| `workload.security.podSecurityContext.runAsUser` | `65532` | |
| `workload.security.podSecurityContext.fsGroup` | `65532` | |
| `workload.security.podSecurityContext.seccompProfile.type` | `RuntimeDefault` | |
| `workload.security.containerSecurityContext.allowPrivilegeEscalation` | `false` | Container-level security context applied to every workload container, including the `code-loader` init container in deploy steps. |
| `workload.security.containerSecurityContext.capabilities.drop` | `["ALL"]` | |
| `workload.security.containerSecurityContext.readOnlyRootFilesystem` | `true` | |

### Code source (deploy steps)

Controls how the chain controller resolves and polls fusion-index for `codeSource`-backed
deploy steps (WeaveServiceTemplate `spec.codeSource`).

| Key | Default | Description |
|---|---|---|
| `codeSource.pollInterval` | `60s` | Go duration string. How often the chain controller polls fusion-index for artifact tag changes. Maps to `CODE_SOURCE_POLL_INTERVAL`. |
| `codeSource.indexURL` | `""` | Cluster-wide default fusion-index base URL, applied to any deploy step whose WeaveServiceTemplate does not set its own `codeSource.indexURL`. Maps to `FUSION_INDEX_URL`. Empty uses the built-in default `http://fusion-index-backend.fusion.svc.cluster.local:8080`. |
| `codeSource.loaderImage` | `""` | Cluster-wide default init container image for code-source deploy steps, applied when a WeaveServiceTemplate does not set its own `codeSource.loaderImage`. Maps to `LOADER_IMAGE`. Empty uses the built-in default `fusion-code-loader:latest` — does not exist on minikube, so set explicitly (e.g. to the operator image, which embeds `/loader`). |
| `codeSource.writablePaths` | `[/tmp, /home/nonroot, /weave-work]` | Paths that receive a writable `emptyDir` volume in both the `code-loader` init container and the main container, for deploy steps that have a `codeSource`. Required when `readOnlyRootFilesystem: true` (workload security default above) so runners can extract archives, install dependencies, and write cache/temp files. |

### Ingress

| Key | Default | Description |
|---|---|---|
| `ingress.hostSuffix` | `""` | Cluster-wide domain suffix appended to every ingress rule's `name` to form the full Ingress hostname: `<name>.<hostSuffix>`. Users configuring WeaveServiceTemplate/WeaveRun ingress can only supply the leftmost DNS label, never a full domain, so an Ingress can never be pointed at a hostname the operator doesn't own. Maps to `INGRESS_HOST_SUFFIX`. A WeaveServiceTemplate with `spec.ingress` set stays `status.valid=false` until this is set. |

### Backup

Periodic S3 backup of WeaveJobTemplate/WeaveServiceTemplate/WeaveChain/WeaveTrigger specs
(never WeaveRun, never `.status`) via a daily CronJob — see `cmd/backup`. Restore is
manual-only (`/backup restore`), not chart-templated.

| Key | Default | Description |
|---|---|---|
| `backup.enabled` | `false` | Deploy the backup CronJob and its ServiceAccount/Role/RoleBinding. |
| `backup.schedule` | `0 3 * * *` | Standard cron expression (`CronJob.spec.schedule`). |
| `backup.successfulJobsHistoryLimit` | `3` | Number of successful backup Jobs kept. |
| `backup.failedJobsHistoryLimit` | `1` | Number of failed backup Jobs kept. |
| `backup.activeDeadlineSeconds` | `1800` | Maximum runtime for a single backup Job before it's killed. |
| `backup.image.repository` | `""` | Override image repository for the backup CronJob. Empty inherits `image.repository`. |
| `backup.image.tag` | `""` | Override image tag for the backup CronJob. Empty inherits `image.tag`. |
| `backup.image.pullPolicy` | `""` | Override pull policy for the backup CronJob. Empty inherits `image.pullPolicy`. |
| `backup.serviceAccount.name` | `fusion-weave-backup` | ServiceAccount name for the backup CronJob; read-only access to the 4 backed-up CRD types (never weaveruns). |
| `backup.s3.bucket` | `""` | **Required** when `backup.enabled=true`. S3 bucket name. |
| `backup.s3.backupPrefix` | `""` | S3 key prefix for backup objects. Empty resolves to `<kubernetes-namespace>/backups`. |
| `backup.s3.region` | `us-east-1` | AWS region. |
| `backup.s3.endpointOverride` | `""` | S3-compatible endpoint override, for MinIO, Ceph, or other custom endpoints. |
| `backup.s3.credentialsType` | `static` | `static` injects `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` from a Secret (`accessKeyId`/`secretAccessKey` below, or `existingSecret`); `default` relies on the AWS SDK's default credential chain (IRSA, instance role, etc). |
| `backup.s3.existingSecret` | `""` | Name of an existing Secret with `aws-access-key-id`/`aws-secret-access-key` keys. When set, the chart does not create `<release>-backup-s3-secret`. |
| `backup.s3.accessKeyId` | `""` | Static AWS access key ID. Required when `credentialsType=static` and `existingSecret` is empty. |
| `backup.s3.secretAccessKey` | `""` | Static AWS secret access key. Required when `credentialsType=static` and `existingSecret` is empty. |

### Showroom

A curated, preseeded set of example WeaveChains/WeaveTriggers touring the platform's
major features — `deployment/fusion-weave/templates/showroom/`, one file per chain.

**Tier 1** (`showroom.chains.*`) is fully self-contained (busybox/nginx images only,
no external dependency) — installs and runs immediately:

| Key | Default | Description |
|---|---|---|
| `showroom.enabled` | `false` | Master switch for the showroom. Individual chains below are still gated by their own flag. |
| `showroom.chains.dagBasics` | `true` | Fan-out/fan-in DAG + a failure-only cleanup branch (`showroom-dag-basics`). |
| `showroom.chains.sharedStorage` | `true` | Two parallel writers + a reader proving `sharedStorage` persists across steps (`showroom-shared-storage`). |
| `showroom.chains.deployIngress` | `true` | build → deploy (nginx, Deploy-kind step + `WeaveIngressRule`) → smoke-test (`showroom-deploy-ingress`). Requires `ingress.hostSuffix` set, or the WeaveServiceTemplate stays `status.valid=false`. |
| `showroom.chains.stepIO` | `true` | fetch → validate using `producesOutput`/`consumesOutputFrom` (`showroom-step-io`). |
| `showroom.chains.triggerOnDemand` | `true` | `OnDemand` trigger attached to `showroom-dag-basics`. |
| `showroom.chains.triggerCron` | `true` | `Cron` trigger (6-field, seconds-first) attached to `showroom-dag-basics`. |
| `showroom.chains.triggerBatchCron` | `true` | `BatchCron` trigger + ConfigMap-driven job list (`showroom-batch-cron`); genuinely fireable, no external broker needed. |
| `showroom.chains.triggerKafka` | `false` | `Kafka` trigger (`showroom-s3-ingest`); only fires against a reachable broker — off by default. Point `showroom.kafka.brokers`/`topic` at your own instance (matches `deployment/local-dev/redpanda-values.yaml` conventions) before enabling. |
| `showroom.sharedStorage.size` | `500Mi` | PVC size for the sharedStorage demo chain. |
| `showroom.sharedStorage.storageClassName` | `""` | StorageClass to use (must support ReadWriteMany). On minikube: `csi-hostpath-sc` (requires `minikube addons enable csi-hostpath-driver`). Leave empty to use the cluster default. |
| `showroom.deployIngress.ingressName` | `showroom` | DNS-label hostname prefix for the deployIngress demo; full host is `<ingressName>.<ingress.hostSuffix>`. |
| `showroom.kafka.brokers` | `[redpanda-0.redpanda.redpanda.svc.cluster.local:9093]` | Broker list for the triggerKafka demo. |
| `showroom.kafka.topic` | `s3-events` | Topic for the triggerKafka demo. |
| `showroom.kafka.consumerGroup` | `fusion-weave-showroom` | Consumer group for the triggerKafka demo. |

**Tier 2** (`showroom.codeSourceApps.*`) deploys real artifacts built from
`../fusion-testcases/testcases_v2/` via fusion-forge and published to fusion-index
under tag `stable` — see `testcases_v2/README.md`. Off by default so Tier 1 still
installs standalone; enable only once those artifacts already exist:

| Key | Default | Description |
|---|---|---|
| `showroom.codeSourceApps.enabled` | `false` | Master switch for Tier 2. |
| `showroom.codeSourceApps.streamlitShowcase` | `true` | Deploys `app.streamlit-showcase` via `codeSource` on a Deploy-kind step (`showroom-streamlit-showcase`). |
| `showroom.codeSourceApps.batchReport` | `true` | Runs `app.batch-report-generator` via `codeSource` on a Job-kind step, `producesOutput: true` (`showroom-batch-report`), fired `OnDemand`. |
| `showroom.codeSourceApps.batchMetadata` | `true` | Runs `app.batch-metadata-reader` via `codeSource` on a Job-kind step (`showroom-batch-metadata`), fired by a **`BatchCron`** trigger (`showroom-batch-metadata-cron`, ConfigMap `showroom-batch-metadata-jobs`) instead of `OnDemand` — the step reads back both its own artifact metadata (`WEAVE_*`) and the firing job's `JOB_*`/`JOB_METADATA` env vars, tying `codeSource` and `BatchCron` together. |

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
| Service | `<release>-api` | Exposes API port and metrics port (when `api.enabled=true`; metrics port only when `api.monitoring.enabled=true`) |
| ServiceAccount | `fusion-weave-backup` | Backup CronJob identity (when `backup.enabled=true`) |
| Role | `fusion-weave-backup` | Read-only on WeaveJobTemplate/WeaveServiceTemplate/WeaveChain/WeaveTrigger — never weaveruns (when `backup.enabled=true`) |
| RoleBinding | `fusion-weave-backup` | Binds role to SA (when `backup.enabled=true`) |
| Secret | `<release>-backup-s3-secret` | Static AWS credentials (when `backup.enabled=true`, `backup.s3.credentialsType=static`, and `backup.s3.existingSecret` is unset) |
| CronJob | `<release>-backup` | Daily S3 backup of CRD specs (when `backup.enabled=true`) |
