# fusion-weave REST API Reference

The fusion-weave REST API exposes full CRUD operations for all five CRDs over HTTP/JSON. It runs as a separate process (`/api-server`) on port `8082`.

---

## Base URL

```
http://<host>:8082
```

When accessing locally via port-forward:

```bash
kubectl port-forward svc/fusion-weave-api 8082:8082 -n fusion
```

---

## Authentication

All endpoints under `/api/v1` require authentication unless `AllowUnauthenticated=true` is set (development only).

Pass credentials as a Bearer token:

```
Authorization: Bearer <token>
```

### API key

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/chains
```

Keys are Kubernetes Secrets labeled `fusion-platform.io/api-key=true`. The role is read from the annotation `fusion-platform.io/role`.

### OIDC JWT

```bash
TOKEN=$(gcloud auth print-identity-token)
curl -H "Authorization: Bearer $TOKEN" http://localhost:8082/api/v1/chains
```

### ServiceAccount token

```bash
TOKEN=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)
curl -H "Authorization: Bearer $TOKEN" http://localhost:8082/api/v1/chains
```

### Roles

| Role | Allowed methods |
|---|---|
| `viewer` | GET |
| `editor` | GET, POST, PUT, PATCH |
| `admin` | GET, POST, PUT, PATCH, DELETE |

---

## Error Responses

All errors return JSON with the following shape:

```json
{
  "code": 404,
  "message": "resource not found"
}
```

| Status | Meaning |
|---|---|
| `400` | Malformed JSON body or missing required field |
| `401` | Missing or invalid credentials |
| `403` | Valid credentials but insufficient role |
| `404` | Resource does not exist |
| `405` | Method not allowed |
| `500` | Internal server error (Kubernetes API unreachable) |
| `503` | Auth service unavailable (OIDC JWKS discovery failed) |

---

## PATCH semantics

`PATCH` uses **JSON Merge Patch** (`RFC 7396`). Send only the fields you want to change. Omitted fields are left unchanged. To remove an optional field set it to `null`.

```
Content-Type: application/merge-patch+json
```

---

## Health Endpoints

These endpoints do not require authentication.

### GET /healthz

Returns `200 OK` when the API server process is running.

```bash
curl http://localhost:8082/healthz
```

```json
{"status":"ok"}
```

### GET /readyz

Returns `200 OK` when the API server can reach the Kubernetes API.

```bash
curl http://localhost:8082/readyz
```

```json
{"status":"ok"}
```

---

## WeaveJobTemplate

`/api/v1/jobtemplates`

Defines a reusable job container spec. Steps in a WeaveChain reference these by name.

### Fields

```json
{
  "apiVersion": "weave.fusion-platform.io/v1alpha1",
  "kind": "WeaveJobTemplate",
  "metadata": {
    "name": "echo-hello",
    "namespace": "fusion"
  },
  "spec": {
    "image": "alpine:3.19",
    "command": ["/bin/sh"],
    "args": ["-c", "echo hello"],
    "env": [
      {"name": "LOG_LEVEL", "value": "debug"}
    ],
    "resources": {
      "requests": {"cpu": "100m", "memory": "64Mi"},
      "limits":   {"cpu": "500m", "memory": "128Mi"}
    },
    "volumes": [
      {
        "name": "config",
        "mountPath": "/etc/config",
        "configMapName": "my-config"
      }
    ],
    "retryPolicy": {
      "maxRetries": 3,
      "backoffSeconds": 10
    },
    "parallelism": 1,
    "completions": 1,
    "activeDeadlineSeconds": 300,
    "serviceAccountName": "my-sa",
    "podSecurityContext": {
      "runAsNonRoot": true,
      "runAsUser": 1000,
      "fsGroup": 1000
    },
    "containerSecurityContext": {
      "allowPrivilegeEscalation": false,
      "readOnlyRootFilesystem": true
    },
    "codeSource": {
      "artifactName": "app.my-batch-job",
      "tag": "stable",
      "mountPath": "/weave-code",
      "indexURL": "http://fusion-index-backend.fusion.svc.cluster.local:8080",
      "loaderImage": "fusion-weave-operator:0.4.0",
      "loaderImagePullPolicy": "IfNotPresent"
    }
  }
}
```

`image` is required. All other fields are optional.

`volumes` entries accept exactly one of `secretName` or `configMapName`:

```json
{"name": "creds", "mountPath": "/etc/creds", "secretName": "my-secret"}
```

`podSecurityContext` / `containerSecurityContext`, when set, override the operator-wide `WORKLOAD_SECURITY_DEFAULTS` entirely for pods created from this template — they accept the full `corev1.PodSecurityContext` / `corev1.SecurityContext` field sets (`runAsUser`, `runAsGroup`, `fsGroup`, `seccompProfile`, `allowPrivilegeEscalation`, `capabilities`, etc.).

`codeSource`, when set, injects a `code-loader` init container that resolves `tag` to a concrete artifact version via fusion-index and copies the artifact's files as-is into `mountPath` (default `/weave-code`) before the job container starts. `artifactName` and `tag` are required when `codeSource` is set; `indexURL` falls back to the `FUSION_INDEX_URL` env var, then to the in-cluster default. Because every `WeaveRun` creates a fresh Job pod, the tag is re-resolved to its current version on every run — unlike Deploy-kind `codeSource` (see WeaveServiceTemplate below), no polling or rolling-restart mechanism is needed.

### List

```
GET /api/v1/jobtemplates
```

Returns all WeaveJobTemplates in the managed namespace.

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/jobtemplates
```

```json
{
  "apiVersion": "weave.fusion-platform.io/v1alpha1",
  "kind": "WeaveJobTemplateList",
  "items": [
    {
      "apiVersion": "weave.fusion-platform.io/v1alpha1",
      "kind": "WeaveJobTemplate",
      "metadata": {"name": "echo-hello", "namespace": "fusion", ...},
      "spec": {"image": "alpine:3.19", "command": ["/bin/sh"], ...},
      "status": {"observedGeneration": 1, "valid": true, "validationMessage": ""}
    }
  ]
}
```

### Create

```
POST /api/v1/jobtemplates
Content-Type: application/json
```

```bash
curl -X POST http://localhost:8082/api/v1/jobtemplates \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveJobTemplate",
    "metadata": {"name": "echo-hello", "namespace": "fusion"},
    "spec": {
      "image": "alpine:3.19",
      "command": ["/bin/sh"],
      "args": ["-c", "echo hello"]
    }
  }'
```

Returns `201 Created` with the full object including server-populated metadata.

### Get

```
GET /api/v1/jobtemplates/{name}
```

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/jobtemplates/echo-hello
```

```json
{
  "apiVersion": "weave.fusion-platform.io/v1alpha1",
  "kind": "WeaveJobTemplate",
  "metadata": {"name": "echo-hello", "namespace": "fusion", ...},
  "spec": {"image": "alpine:3.19", ...},
  "status": {"observedGeneration": 1, "valid": true}
}
```

### Update (full replace)

```
PUT /api/v1/jobtemplates/{name}
Content-Type: application/json
```

Replaces the entire spec. You must include `metadata.resourceVersion` to prevent lost-update races.

```bash
curl -X PUT http://localhost:8082/api/v1/jobtemplates/echo-hello \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveJobTemplate",
    "metadata": {
      "name": "echo-hello",
      "namespace": "fusion",
      "resourceVersion": "12345"
    },
    "spec": {
      "image": "alpine:3.20",
      "command": ["/bin/sh"],
      "args": ["-c", "echo updated"],
      "retryPolicy": {"maxRetries": 5, "backoffSeconds": 30}
    }
  }'
```

### Patch (partial update)

```
PATCH /api/v1/jobtemplates/{name}
Content-Type: application/merge-patch+json
```

```bash
curl -X PATCH http://localhost:8082/api/v1/jobtemplates/echo-hello \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/merge-patch+json" \
  -d '{"spec": {"image": "alpine:3.20"}}'
```

### Delete

```
DELETE /api/v1/jobtemplates/{name}
```

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" \
  http://localhost:8082/api/v1/jobtemplates/echo-hello
```

Returns `200 OK` on success.

---

## WeaveServiceTemplate

`/api/v1/servicetemplates`

Defines a long-running service (Deployment + Service + optional Ingress). Used by `stepKind: Deploy` steps.

### Fields

```json
{
  "apiVersion": "weave.fusion-platform.io/v1alpha1",
  "kind": "WeaveServiceTemplate",
  "metadata": {"name": "my-api", "namespace": "fusion"},
  "spec": {
    "image": "nginx:1.25",
    "command": [],
    "args": [],
    "env": [{"name": "PORT", "value": "8080"}],
    "resources": {
      "requests": {"cpu": "100m", "memory": "128Mi"},
      "limits":   {"cpu": "1",    "memory": "256Mi"}
    },
    "volumes": [],
    "serviceAccountName": "",
    "podSecurityContext": {
      "runAsNonRoot": true,
      "runAsUser": 1000,
      "fsGroup": 1000
    },
    "containerSecurityContext": {
      "allowPrivilegeEscalation": false,
      "readOnlyRootFilesystem": true
    },
    "replicas": 2,
    "ports": [
      {"name": "http", "port": 80, "targetPort": 8080, "protocol": "TCP"}
    ],
    "livenessProbe": {
      "httpGet": {"path": "/healthz", "port": 8080},
      "initialDelaySeconds": 5,
      "periodSeconds": 10
    },
    "readinessProbe": {
      "httpGet": {"path": "/readyz", "port": 8080},
      "initialDelaySeconds": 3,
      "periodSeconds": 5
    },
    "serviceType": "ClusterIP",
    "ingress": {
      "ingressClassName": "nginx",
      "rules": [
        {
          "name": "my-api",
          "path": "/",
          "pathType": "Prefix",
          "servicePort": 80
        }
      ],
      "tlsSecretName": "my-api-tls"
    },
    "unhealthyDuration": "5m",
    "revisionHistoryLimit": 3,
    "codeSource": {
      "artifactName": "app.my-api",
      "tag": "stable",
      "mountPath": "/weave-code",
      "indexURL": "http://fusion-index-backend.fusion.svc.cluster.local:8080",
      "loaderImage": "fusion-weave-operator:0.4.0",
      "loaderImagePullPolicy": "IfNotPresent"
    }
  }
}
```

`image` and at least one entry in `ports` are required.

`ingress.rules[].name` is a DNS label only (e.g. `my-api`), **not** a full hostname — the API cannot be used to point an Ingress at an arbitrary domain. The operator appends the cluster-wide `ingress.hostSuffix` (set once at install time) to build the real hostname, e.g. `my-api.svc.instance-a.fusion.company.com`. A template with `ingress` set is rejected (`status.valid=false`) until the operator has a suffix configured. The same applies to `WeaveRun.spec.stepOverrides[].ingressName`.

`serviceType` accepts `ClusterIP`, `NodePort`, or `LoadBalancer` (default `ClusterIP`).

`podSecurityContext` / `containerSecurityContext`, when set, override the operator-wide `WORKLOAD_SECURITY_DEFAULTS` entirely for pods created from this template (`containerSecurityContext` applies to both the service container and the `code-loader` init container when `codeSource` is configured) — they accept the full `corev1.PodSecurityContext` / `corev1.SecurityContext` field sets.

`codeSource`, when set, injects a `code-loader` init container that resolves `tag` to a concrete artifact version via fusion-index and copies the artifact's files as-is into `mountPath` (default `/weave-code`) before the main container starts. `artifactName` and `tag` are required when `codeSource` is set; `indexURL` falls back to the `FUSION_INDEX_URL` env var, then to the in-cluster default. Unlike Job-kind steps, the init container fires on every pod start, so a running Deployment does not pick up a new tagged version automatically — trigger a rolling restart with `kubectl annotate weavechain <chain-name> fusion-platform.io/reload-deploy-step=<stepName>@<version> --overwrite -n fusion` (or the chain controller's own tag-polling loop, `codeSource.pollInterval`).

### List

```
GET /api/v1/servicetemplates
```

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/servicetemplates
```

### Create

```
POST /api/v1/servicetemplates
Content-Type: application/json
```

```bash
curl -X POST http://localhost:8082/api/v1/servicetemplates \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveServiceTemplate",
    "metadata": {"name": "my-api", "namespace": "fusion"},
    "spec": {
      "image": "nginx:1.25",
      "replicas": 1,
      "ports": [{"name": "http", "port": 80, "targetPort": 8080}]
    }
  }'
```

### Get

```
GET /api/v1/servicetemplates/{name}
```

### Update

```
PUT /api/v1/servicetemplates/{name}
Content-Type: application/json
```

### Patch

```
PATCH /api/v1/servicetemplates/{name}
Content-Type: application/merge-patch+json
```

Scale replicas without touching other fields:

```bash
curl -X PATCH http://localhost:8082/api/v1/servicetemplates/my-api \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/merge-patch+json" \
  -d '{"spec": {"replicas": 3}}'
```

### Delete

```
DELETE /api/v1/servicetemplates/{name}
```

---

## WeaveChain

`/api/v1/chains`

Defines the DAG of steps (job or deploy) that execute together as a pipeline.

### Fields

```json
{
  "apiVersion": "weave.fusion-platform.io/v1alpha1",
  "kind": "WeaveChain",
  "metadata": {"name": "etl-pipeline", "namespace": "fusion"},
  "spec": {
    "steps": [
      {
        "name": "extract",
        "stepKind": "Job",
        "jobTemplateRef": {"name": "extractor"},
        "producesOutput": true
      },
      {
        "name": "transform",
        "stepKind": "Job",
        "jobTemplateRef": {"name": "transformer"},
        "dependsOn": ["extract"],
        "consumesOutputFrom": ["extract"],
        "envOverrides": [{"name": "BATCH_SIZE", "value": "500"}]
      },
      {
        "name": "load",
        "stepKind": "Job",
        "jobTemplateRef": {"name": "loader"},
        "dependsOn": ["transform"],
        "consumesOutputFrom": ["transform"]
      },
      {
        "name": "api-server",
        "stepKind": "Deploy",
        "serviceTemplateRef": {"name": "my-api"},
        "dependsOn": ["load"]
      },
      {
        "name": "notify-failure",
        "stepKind": "Job",
        "jobTemplateRef": {"name": "notifier"},
        "dependsOn": ["transform"],
        "runOnSuccess": false,
        "runOnFailure": true
      }
    ],
    "failurePolicy": "StopAll",
    "concurrencyPolicy": "Wait",
    "sharedStorage": {
      "size": "1Gi",
      "storageClassName": "csi-hostpath-sc"
    },
    "authSecretRef": {"name": "keycloak-service-creds"}
  }
}
```

**`steps[].stepKind`**: `Job` (default) or `Deploy`. Job steps use `jobTemplateRef`; deploy steps use `serviceTemplateRef`.

**`steps[].dependsOn`**: list of step names that must complete first.

**`steps[].runOnSuccess`** / **`runOnFailure`**: control conditional execution (default `runOnSuccess: true`, `runOnFailure: false`).

**`steps[].consumesOutputFrom`**: every listed step must have `producesOutput: true` and be an ancestor in the DAG — validated at admission.

**`failurePolicy`**: `StopAll` (default), `ContinueOthers`, `RetryFailed`.

**`concurrencyPolicy`**: `Wait` (default — queue new runs) or `Forbid` (skip if one is already running).

**`sharedStorage`**: mounts a per-run RWX PVC at `/weave-shared` in every job pod. Requires a StorageClass that supports ReadWriteMany.

**`authSecretRef`**: optionally names a Secret injected via `envFrom` into every step pod of the chain (Job and Deploy kind alike). Its keys become environment variables that runner-side helper libraries (e.g. the `fusion-runner` Python `KeycloakAuth` helper) use to obtain access tokens. Overridable per-trigger (`WeaveTriggerSpec.authSecretRefOverride`) and per-run (`WeaveRunSpec.authSecretRefOverride`); precedence is run > trigger > chain.

### List

```
GET /api/v1/chains
```

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/chains
```

### Create

```
POST /api/v1/chains
Content-Type: application/json
```

```bash
curl -X POST http://localhost:8082/api/v1/chains \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveChain",
    "metadata": {"name": "etl-pipeline", "namespace": "fusion"},
    "spec": {
      "steps": [
        {
          "name": "extract",
          "jobTemplateRef": {"name": "extractor"}
        },
        {
          "name": "load",
          "jobTemplateRef": {"name": "loader"},
          "dependsOn": ["extract"]
        }
      ]
    }
  }'
```

### Get

```
GET /api/v1/chains/{name}
```

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/chains/etl-pipeline
```

### Update

```
PUT /api/v1/chains/{name}
Content-Type: application/json
```

### Patch

```
PATCH /api/v1/chains/{name}
Content-Type: application/merge-patch+json
```

Change failure policy without rewriting the full spec:

```bash
curl -X PATCH http://localhost:8082/api/v1/chains/etl-pipeline \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/merge-patch+json" \
  -d '{"spec": {"failurePolicy": "ContinueOthers"}}'
```

### Delete

```
DELETE /api/v1/chains/{name}
```

---

## WeaveTrigger

`/api/v1/triggers`

Attaches scheduling or external-event firing to a WeaveChain.

### Fields

```json
{
  "apiVersion": "weave.fusion-platform.io/v1alpha1",
  "kind": "WeaveTrigger",
  "metadata": {
    "name": "daily-etl",
    "namespace": "fusion"
  },
  "spec": {
    "chainRef": {"name": "etl-pipeline"},
    "type": "Cron",
    "schedule": "0 0 2 * * *",
    "paused": false,
    "authSecretRefOverride": {"name": "keycloak-service-creds"},
    "parameterOverrides": [
      {"name": "ENV", "value": "production"}
    ]
  }
}
```

**`type`**: `OnDemand`, `Cron`, `Webhook`, `BatchCron`, or `Kafka`. `BatchCron` and `Kafka` also have dedicated convenience endpoints under `/api/v1/batchtriggers` and `/api/v1/kafkatriggers` — see below.

**`paused`**: suspends scheduling for any trigger type when `true`. Runs already in progress are unaffected; no new runs are created until set back to `false`.

**`authSecretRefOverride`**: overrides `WeaveChainSpec.authSecretRef` for every run created by this trigger. Takes precedence over the chain default; yields to `WeaveRunSpec.authSecretRefOverride` on the resulting run.

**Cron trigger** — requires `schedule` (6-field, seconds-first cron expression — `internal/trigger` uses `cron.WithSeconds()`, so this is **not** standard 5-field cron):

```json
{
  "spec": {
    "chainRef": {"name": "etl-pipeline"},
    "type": "Cron",
    "schedule": "0 */15 * * * *"
  }
}
```

**OnDemand trigger** — fire by annotating the trigger object:

```bash
kubectl annotate weavetrigger daily-etl fusion-platform.io/fire=true -n fusion
```

Or via the REST API PATCH:

```bash
curl -X PATCH http://localhost:8082/api/v1/triggers/daily-etl \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/merge-patch+json" \
  -d '{
    "metadata": {
      "annotations": {"fusion-platform.io/fire": "true"}
    }
  }'
```

**Webhook trigger** — the operator listens on port `9090`:

```json
{
  "spec": {
    "chainRef": {"name": "etl-pipeline"},
    "type": "Webhook",
    "webhook": {
      "path": "/hooks/etl",
      "secretRef": {"name": "webhook-token"}
    }
  }
}
```

Fire with:

```bash
curl -X POST http://<cluster>:9090/hooks/etl \
  -H "Authorization: Bearer $(kubectl get secret webhook-token -n fusion -o jsonpath='{.data.token}' | base64 -d)"
```

**BatchCron trigger** — fires individual jobs from a YAML job list stored in a ConfigMap; each job carries its own cron schedule and metadata injected as env vars. Prefer the dedicated `/api/v1/batchtriggers` endpoints below, which also manage the backing ConfigMap; this raw form is shown for completeness:

```json
{
  "spec": {
    "chainRef": {"name": "etl-pipeline"},
    "type": "BatchCron",
    "batchCron": {
      "jobsConfigMapRef": {"name": "batchtrigger-daily-etl"}
    }
  }
}
```

Note: schedules inside the ConfigMap's `jobs.yaml` are standard 5-field cron (no seconds field) — see the `/api/v1/batchtriggers` section below for the YAML format.

**Kafka trigger** — fires one WeaveRun per consumed message, after applying `eventFilter`/`bucketFilter`:

```json
{
  "spec": {
    "chainRef": {"name": "etl-pipeline"},
    "type": "Kafka",
    "kafka": {
      "brokers": ["redpanda-0.redpanda.redpanda.svc.cluster.local:9093"],
      "topic": "s3-events",
      "consumerGroup": "etl-pipeline-consumer",
      "secretRef": {"name": "kafka-sasl-creds"},
      "eventFilter": ["put"],
      "bucketFilter": ["uploads"],
      "maxConcurrentRuns": 5
    }
  }
}
```

`kafka.brokers`, `kafka.topic`, and `kafka.consumerGroup` are required. `kafka.secretRef` optionally names a Secret with keys `username`, `password`, and optionally `mechanism` (`PLAIN`, `SCRAM-SHA-256`, `SCRAM-SHA-512`; defaults to `PLAIN`). `kafka.eventFilter` accepts `put`, `delete`, `get` (empty = all events); `kafka.bucketFilter` restricts by S3 bucket name (empty = all buckets). `kafka.maxConcurrentRuns` caps active WeaveRuns for this trigger — events received at the cap are skipped with the offset still committed (`0` = unlimited). Prefer the dedicated `/api/v1/kafkatriggers` endpoints below.

### List

```
GET /api/v1/triggers
```

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/triggers
```

### Create

```
POST /api/v1/triggers
Content-Type: application/json
```

```bash
curl -X POST http://localhost:8082/api/v1/triggers \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveTrigger",
    "metadata": {"name": "daily-etl", "namespace": "fusion"},
    "spec": {
      "chainRef": {"name": "etl-pipeline"},
      "type": "Cron",
      "schedule": "0 0 2 * * *"
    }
  }'
```

### Get

```
GET /api/v1/triggers/{name}
```

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/triggers/daily-etl
```

### Update

```
PUT /api/v1/triggers/{name}
Content-Type: application/json
```

### Patch

```
PATCH /api/v1/triggers/{name}
Content-Type: application/merge-patch+json
```

Update cron schedule:

```bash
curl -X PATCH http://localhost:8082/api/v1/triggers/daily-etl \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/merge-patch+json" \
  -d '{"spec": {"schedule": "0 0 4 * * *"}}'
```

### Delete

```
DELETE /api/v1/triggers/{name}
```

---

## BatchCron Triggers

`/api/v1/batchtriggers`

Dedicated CRUD + lifecycle endpoints for `WeaveTrigger` objects of `type: BatchCron`. Unlike the generic `/api/v1/triggers` endpoints, these also manage the backing ConfigMap that holds the job list YAML — creating a batch trigger creates both the `WeaveTrigger` (owning) and a ConfigMap named `batchtrigger-<name>` (key `jobs.yaml`) in one call, and updating replaces that ConfigMap's contents. List/Get/Delete operate on the same underlying `WeaveTrigger` objects as `/api/v1/triggers`, filtered to `type: BatchCron`.

### Fields

The `jobs` string is YAML: a sequence of `job` entries, each with its own cron schedule (standard 5-field, no seconds — different from the `Cron` trigger type's 6-field seconds-first `schedule`) and metadata injected into the fired job's pod as `JOB_ID`, `JOB_NAME`, `JOB_TOPIC`, `JOB_MAINTAINER`, `JOB_STARTDATE`, `JOB_STARTTIME`, `JOB_SCHEDULE`, and `JOB_METADATA` (full metadata JSON) env vars:

```json
{
  "name": "daily-etl",
  "chainRef": {"name": "etl-pipeline"},
  "jobs": "- job:\n    id: \"nightly-export\"\n    name: \"nightly-export\"\n    topic: \"reporting\"\n    maintainer: \"data-team\"\n    startdate: \"2026-04-12\"\n    starttime: \"02:00\"\n    schedule: \"0 2 * * *\"\n    metadata:\n      kind: export\n"
}
```

`name`, `chainRef.name`, and `jobs` are required on Create. Each `job` entry requires `id` and `schedule`; `startdate`/`starttime` (format `YYYY-MM-DD` / `HH:MM`) optionally delay the job's first eligible fire.

### List

```
GET /api/v1/batchtriggers
```

Returns a `WeaveTriggerList` filtered to `type: BatchCron` — same item shape as `GET /api/v1/triggers`.

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/batchtriggers
```

### Create

```
POST /api/v1/batchtriggers
Content-Type: application/json
```

```bash
curl -X POST http://localhost:8082/api/v1/batchtriggers \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "daily-etl",
    "chainRef": {"name": "etl-pipeline"},
    "jobs": "- job:\n    id: \"nightly-export\"\n    schedule: \"0 2 * * *\"\n"
  }'
```

Returns `201 Created` with the full `WeaveTrigger` object. If the job YAML fails validation, returns `422 Unprocessable Entity` with `{"valid": false, "errors": [{"line": 2, "message": "..."}]}` and creates nothing. If the ConfigMap create fails after the `WeaveTrigger` was already created, the handler best-effort deletes the `WeaveTrigger` to avoid an orphaned/stuck object.

### Get

```
GET /api/v1/batchtriggers/{name}
```

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/batchtriggers/daily-etl
```

### Update (replace job list)

```
PUT /api/v1/batchtriggers/{name}
Content-Type: application/json
```

Replaces the `jobs.yaml` contents of the backing ConfigMap. Re-validated the same way as Create.

```bash
curl -X PUT http://localhost:8082/api/v1/batchtriggers/daily-etl \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "jobs": "- job:\n    id: \"nightly-export\"\n    schedule: \"0 3 * * *\"\n"
  }'
```

### Patch

```
PATCH /api/v1/batchtriggers/{name}
Content-Type: application/merge-patch+json
```

Applies a JSON Merge Patch directly to the `WeaveTrigger` object (not the ConfigMap) — use this for metadata/annotation changes, not job-list edits (use Update or Resume for those).

```bash
curl -X PATCH http://localhost:8082/api/v1/batchtriggers/daily-etl \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/merge-patch+json" \
  -d '{"metadata": {"labels": {"env": "staging"}}}'
```

### Delete

```
DELETE /api/v1/batchtriggers/{name}
```

Deletes the `WeaveTrigger`; the backing ConfigMap is garbage-collected automatically via its owner reference.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" \
  http://localhost:8082/api/v1/batchtriggers/daily-etl
```

### Stop

```
POST /api/v1/batchtriggers/{name}/stop
```

Sets `spec.paused = true`, suspending all scheduling for this trigger. Returns `409 Conflict` if already paused.

```bash
curl -X POST http://localhost:8082/api/v1/batchtriggers/daily-etl/stop \
  -H "Authorization: Bearer $KEY"
```

### Resume

```
POST /api/v1/batchtriggers/{name}/resume
Content-Type: application/json
```

Uploads a new job list YAML and sets `spec.paused = false` in one call.

```bash
curl -X POST http://localhost:8082/api/v1/batchtriggers/daily-etl/resume \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"jobs": "- job:\n    id: \"nightly-export\"\n    schedule: \"0 2 * * *\"\n"}'
```

### Validate

```
POST /api/v1/batchtriggers/validate
Content-Type: application/json
```

Parses the submitted job list YAML and returns line-level errors without touching Kubernetes — use this to validate before Create/Update/Resume.

```bash
curl -X POST http://localhost:8082/api/v1/batchtriggers/validate \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"jobs": "- job:\n    id: \"nightly-export\"\n    schedule: \"0 2 * * *\"\n"}'
```

```json
{"valid": true}
```

```json
{
  "valid": false,
  "errors": [
    {"line": 2, "message": "id is required"},
    {"line": 4, "message": "invalid schedule \"bad cron\": ..."}
  ]
}
```

---

## Kafka Triggers

`/api/v1/kafkatriggers`

Dedicated CRUD endpoints for `WeaveTrigger` objects of `type: Kafka`. List/Get/Update/Delete operate on the same underlying `WeaveTrigger` objects as `/api/v1/triggers`, filtered to `type: Kafka`. No Stop/Resume/Validate actions — use `PATCH .../{name}` with `{"spec": {"paused": true}}` to pause a Kafka trigger.

### Fields

```json
{
  "name": "s3-upload-notify",
  "chainRef": {"name": "etl-pipeline"},
  "kafka": {
    "brokers": ["redpanda-0.redpanda.redpanda.svc.cluster.local:9093"],
    "topic": "s3-events",
    "consumerGroup": "etl-pipeline-consumer",
    "secretRef": {"name": "kafka-sasl-creds"},
    "eventFilter": ["put"],
    "bucketFilter": ["uploads"],
    "maxConcurrentRuns": 5
  }
}
```

`name`, `chainRef.name`, `kafka.brokers`, `kafka.topic`, and `kafka.consumerGroup` are required. See the WeaveTrigger Fields section above for the meaning of each `kafka.*` sub-field.

### List

```
GET /api/v1/kafkatriggers
```

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/kafkatriggers
```

### Create

```
POST /api/v1/kafkatriggers
Content-Type: application/json
```

```bash
curl -X POST http://localhost:8082/api/v1/kafkatriggers \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "s3-upload-notify",
    "chainRef": {"name": "etl-pipeline"},
    "kafka": {
      "brokers": ["redpanda-0.redpanda.redpanda.svc.cluster.local:9093"],
      "topic": "s3-events",
      "consumerGroup": "etl-pipeline-consumer"
    }
  }'
```

Returns `201 Created` with the full `WeaveTrigger` object.

### Get

```
GET /api/v1/kafkatriggers/{name}
```

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/kafkatriggers/s3-upload-notify
```

### Update

```
PUT /api/v1/kafkatriggers/{name}
Content-Type: application/json
```

Replaces `spec.kafka` in full; `kafka.brokers`, `kafka.topic`, and `kafka.consumerGroup` are required in the request body.

```bash
curl -X PUT http://localhost:8082/api/v1/kafkatriggers/s3-upload-notify \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "kafka": {
      "brokers": ["redpanda-0.redpanda.redpanda.svc.cluster.local:9093"],
      "topic": "s3-events",
      "consumerGroup": "etl-pipeline-consumer",
      "maxConcurrentRuns": 10
    }
  }'
```

### Patch

```
PATCH /api/v1/kafkatriggers/{name}
Content-Type: application/merge-patch+json
```

```bash
curl -X PATCH http://localhost:8082/api/v1/kafkatriggers/s3-upload-notify \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/merge-patch+json" \
  -d '{"spec": {"paused": true}}'
```

### Delete

```
DELETE /api/v1/kafkatriggers/{name}
```

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" \
  http://localhost:8082/api/v1/kafkatriggers/s3-upload-notify
```

---

## WeaveRun

`/api/v1/runs`

Represents a single execution of a WeaveChain. Created automatically by triggers, or manually via the API.

### Fields

```json
{
  "apiVersion": "weave.fusion-platform.io/v1alpha1",
  "kind": "WeaveRun",
  "metadata": {"name": "etl-pipeline-run-1", "namespace": "fusion"},
  "spec": {
    "chainRef": {"name": "etl-pipeline"},
    "triggerRef": {"name": "daily-etl"},
    "parameterOverrides": [
      {"name": "DATE", "value": "2026-04-12"}
    ],
    "stepOverrides": [
      {
        "stepName": "api-server",
        "artifactName": "app.my-service",
        "tag": "stable",
        "ingressName": "my-service-run-1",
        "indexURL": "http://fusion-index-backend.fusion.svc.cluster.local:8080"
      }
    ],
    "authSecretRefOverride": {"name": "keycloak-service-creds"}
  }
}
```

`chainRef` is required and immutable after creation.

`triggerRef` is optional — omit for manually created runs.

`parameterOverrides` values are injected as environment variables into each job pod, taking precedence over the template and chain defaults.

`stepOverrides` provides per-step deployment parameters for `stepKind: Deploy` steps. When a step name is listed here, the operator names the Deployment `<runName>-<stepName>` (owned by the WeaveRun, not the WeaveChain) and reads runner configuration from the artifact's `metadata.yaml` in fusion-index instead of from the `WeaveServiceTemplate` — this lets multiple runs of the same chain deploy independent, non-colliding service instances. `stepName`, `artifactName`, and `tag` are required per entry; `ingressName` (a DNS label, not a full hostname — same rule as `WeaveIngressRule.name`) is required if the chain step declares an Ingress; `indexURL` falls back the same way as `CodeSourceSpec.indexURL`. Steps not listed in `stepOverrides` are unaffected and continue to use the chain-owned `WeaveServiceTemplate` Deployment.

`authSecretRefOverride` overrides `WeaveChainSpec.authSecretRef` (and any `WeaveTriggerSpec.authSecretRefOverride`) for this run only, injected via `envFrom` into every step pod of this run.

### Status

```json
{
  "status": {
    "phase": "Succeeded",
    "startTime": "2026-04-12T02:00:05Z",
    "completionTime": "2026-04-12T02:14:32Z",
    "message": "",
    "sharedPVCName": "etl-pipeline-run-1-shared",
    "steps": [
      {
        "name": "extract",
        "phase": "Succeeded",
        "jobRef": "etl-pipeline-run-1-extract",
        "retryCount": 0,
        "startTime": "2026-04-12T02:00:07Z",
        "completionTime": "2026-04-12T02:03:11Z",
        "outputCaptured": true
      },
      {
        "name": "transform",
        "phase": "Succeeded",
        "jobRef": "etl-pipeline-run-1-transform",
        "retryCount": 1,
        "startTime": "2026-04-12T02:03:15Z",
        "completionTime": "2026-04-12T02:09:02Z",
        "outputCaptured": false
      },
      {
        "name": "load",
        "phase": "Succeeded",
        "jobRef": "etl-pipeline-run-1-load",
        "retryCount": 0,
        "startTime": "2026-04-12T02:09:05Z",
        "completionTime": "2026-04-12T02:14:30Z",
        "outputCaptured": false
      }
    ],
    "activeDeployments": {
      "etl-pipeline-run-1-api-server": {
        "deploymentName": "etl-pipeline-run-1-api-server",
        "stepName": "api-server",
        "health": "Healthy",
        "currentRevision": "1",
        "codeSourceArtifact": "app.my-service",
        "codeSourceTag": "stable",
        "codeSourceIndexURL": "http://fusion-index-backend.fusion.svc.cluster.local:8080",
        "codeSourceDeployedVersion": "1.2.3"
      }
    }
  }
}
```

**`phase`**: `Pending` → `Running` → `Succeeded` | `Failed` | `Stopped`

**`steps[].phase`**: `Pending`, `Running`, `Succeeded`, `Failed`, `Skipped`, `Retrying`, `Deployed`. `Deployed` is non-terminal — it satisfies dependency checks for downstream steps but keeps the WeaveRun in `Running` phase for the lifetime of the service; the run only reaches `Succeeded` once no step is `Deployed`.

**`steps[].outputCaptured`**: `true` when the operator has captured JSON stdout from this step's job.

**`steps[].deploymentRef`**: set instead of `jobRef` for `stepKind: Deploy` steps.

**`activeDeployments`**: present only when this run has `spec.stepOverrides`. Keyed by Deployment name (`<runName>-<stepName>`); tracks the code-source artifact/tag/version currently loaded for run-owned deploy steps, mirroring `WeaveChain.status.activeDeployments` for chain-owned ones.

### List

```
GET /api/v1/runs
```

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/runs
```

Filter by watching (client-side):

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/runs | \
  jq '[.items[] | select(.status.phase == "Failed")]'
```

### Create (manual run)

```
POST /api/v1/runs
Content-Type: application/json
```

```bash
curl -X POST http://localhost:8082/api/v1/runs \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveRun",
    "metadata": {
      "name": "etl-manual-20260412",
      "namespace": "fusion"
    },
    "spec": {
      "chainRef": {"name": "etl-pipeline"},
      "parameterOverrides": [
        {"name": "DATE", "value": "2026-04-12"},
        {"name": "ENV",  "value": "staging"}
      ]
    }
  }'
```

Returns `201 Created`. The operator picks up the new WeaveRun and begins scheduling jobs immediately.

### Get

```
GET /api/v1/runs/{name}
```

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/runs/etl-manual-20260412
```

Poll until complete:

```bash
while true; do
  PHASE=$(curl -s -H "Authorization: Bearer $KEY" \
    http://localhost:8082/api/v1/runs/etl-manual-20260412 | jq -r '.status.phase')
  echo "phase: $PHASE"
  [[ "$PHASE" == "Succeeded" || "$PHASE" == "Failed" ]] && break
  sleep 5
done
```

### Update

```
PUT /api/v1/runs/{name}
Content-Type: application/json
```

Note: `spec.chainRef` is immutable. The Kubernetes API server will reject changes to it.

### Patch

```
PATCH /api/v1/runs/{name}
Content-Type: application/merge-patch+json
```

Add a label to a run:

```bash
curl -X PATCH http://localhost:8082/api/v1/runs/etl-manual-20260412 \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/merge-patch+json" \
  -d '{"metadata": {"labels": {"env": "staging"}}}'
```

### Stop

```
POST /api/v1/runs/{name}/stop
```

Patches `status.phase = Stopped` via the status subresource, triggering the operator's deploy-cleanup finalizer (tears down any `Deployed`-phase steps' Deployment/Service/Ingress) while keeping the run object in history. Returns `409 Conflict` if the run is already in a terminal phase (`Succeeded`, `Failed`, or `Stopped`).

```bash
curl -X POST http://localhost:8082/api/v1/runs/etl-manual-20260412/stop \
  -H "Authorization: Bearer $KEY"
```

### Delete

```
DELETE /api/v1/runs/{name}
```

Deletes the WeaveRun and, via owner references, the associated batch/v1 Jobs and the shared PVC (if present).

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" \
  http://localhost:8082/api/v1/runs/etl-manual-20260412
```

---

## Complete Endpoint Index

| Method | Path | Role required | Description |
|---|---|---|---|
| GET | `/healthz` | none | Liveness check |
| GET | `/readyz` | none | Readiness check |
| GET | `/api/v1/jobtemplates` | viewer | List all WeaveJobTemplates |
| POST | `/api/v1/jobtemplates` | editor | Create a WeaveJobTemplate |
| GET | `/api/v1/jobtemplates/{name}` | viewer | Get a WeaveJobTemplate |
| PUT | `/api/v1/jobtemplates/{name}` | editor | Full replace a WeaveJobTemplate |
| PATCH | `/api/v1/jobtemplates/{name}` | editor | Partial update a WeaveJobTemplate |
| DELETE | `/api/v1/jobtemplates/{name}` | admin | Delete a WeaveJobTemplate |
| GET | `/api/v1/servicetemplates` | viewer | List all WeaveServiceTemplates |
| POST | `/api/v1/servicetemplates` | editor | Create a WeaveServiceTemplate |
| GET | `/api/v1/servicetemplates/{name}` | viewer | Get a WeaveServiceTemplate |
| PUT | `/api/v1/servicetemplates/{name}` | editor | Full replace a WeaveServiceTemplate |
| PATCH | `/api/v1/servicetemplates/{name}` | editor | Partial update a WeaveServiceTemplate |
| DELETE | `/api/v1/servicetemplates/{name}` | admin | Delete a WeaveServiceTemplate |
| GET | `/api/v1/chains` | viewer | List all WeaveChains |
| POST | `/api/v1/chains` | editor | Create a WeaveChain |
| GET | `/api/v1/chains/{name}` | viewer | Get a WeaveChain |
| PUT | `/api/v1/chains/{name}` | editor | Full replace a WeaveChain |
| PATCH | `/api/v1/chains/{name}` | editor | Partial update a WeaveChain |
| DELETE | `/api/v1/chains/{name}` | admin | Delete a WeaveChain |
| GET | `/api/v1/triggers` | viewer | List all WeaveTriggers |
| POST | `/api/v1/triggers` | editor | Create a WeaveTrigger |
| GET | `/api/v1/triggers/{name}` | viewer | Get a WeaveTrigger |
| PUT | `/api/v1/triggers/{name}` | editor | Full replace a WeaveTrigger |
| PATCH | `/api/v1/triggers/{name}` | editor | Partial update a WeaveTrigger |
| DELETE | `/api/v1/triggers/{name}` | admin | Delete a WeaveTrigger |
| GET | `/api/v1/batchtriggers` | viewer | List all BatchCron WeaveTriggers |
| POST | `/api/v1/batchtriggers` | editor | Create a BatchCron trigger + its jobs ConfigMap |
| GET | `/api/v1/batchtriggers/{name}` | viewer | Get a BatchCron trigger |
| PUT | `/api/v1/batchtriggers/{name}` | editor | Replace the job list YAML |
| PATCH | `/api/v1/batchtriggers/{name}` | editor | Partial update the WeaveTrigger object |
| DELETE | `/api/v1/batchtriggers/{name}` | admin | Delete a BatchCron trigger and its ConfigMap |
| POST | `/api/v1/batchtriggers/{name}/stop` | editor | Pause scheduling (`spec.paused=true`) |
| POST | `/api/v1/batchtriggers/{name}/resume` | editor | Upload new job list and resume scheduling |
| POST | `/api/v1/batchtriggers/validate` | editor | Validate job list YAML without touching Kubernetes |
| GET | `/api/v1/kafkatriggers` | viewer | List all Kafka WeaveTriggers |
| POST | `/api/v1/kafkatriggers` | editor | Create a Kafka trigger |
| GET | `/api/v1/kafkatriggers/{name}` | viewer | Get a Kafka trigger |
| PUT | `/api/v1/kafkatriggers/{name}` | editor | Full replace `spec.kafka` |
| PATCH | `/api/v1/kafkatriggers/{name}` | editor | Partial update a Kafka trigger |
| DELETE | `/api/v1/kafkatriggers/{name}` | admin | Delete a Kafka trigger |
| GET | `/api/v1/runs` | viewer | List all WeaveRuns |
| POST | `/api/v1/runs` | editor | Create a WeaveRun (manual trigger) |
| GET | `/api/v1/runs/{name}` | viewer | Get a WeaveRun |
| PUT | `/api/v1/runs/{name}` | editor | Full replace a WeaveRun |
| PATCH | `/api/v1/runs/{name}` | editor | Partial update a WeaveRun |
| POST | `/api/v1/runs/{name}/stop` | editor | Stop a run (`status.phase=Stopped`) |
| DELETE | `/api/v1/runs/{name}` | admin | Delete a WeaveRun and its child resources |
| GET | `/monitor/v1/runs` | viewer | List all WeaveRun summaries |
| GET | `/monitor/v1/runs/{name}` | viewer | Run detail (run + jobs + events) |
| GET | `/monitor/v1/runs/{name}/jobs` | viewer | batch/v1 Jobs for a run |
| GET | `/monitor/v1/runs/{name}/jobs/{jobName}` | viewer | Single batch/v1 Job |
| GET | `/monitor/v1/runs/{name}/steps/{step}/logs` | viewer | Pod log snapshot (last N lines) |
| GET | `/monitor/v1/runs/{name}/events` | viewer | Kubernetes events for a run |
| GET | `/monitor/v1/events` | viewer | All events (optional `?fieldSelector=`) |
| GET | `/monitor/v1/chains/{name}/deployments` | viewer | Deployments owned by a chain |
| GET | `/monitor/v1/stats/runs` | viewer | Aggregated run stats (`?window=`) |
| GET | `/monitor/v1/stats/chains/{name}` | viewer | Per-chain run stats (`?window=`) |

---

## Monitoring API

The monitoring API is enabled separately from the CRUD API (`MONITORING_ENABLED=true`). All monitoring endpoints are read-only (GET) and require at minimum `viewer` role. Responses are served from an in-memory TTL cache (default 30 s). Cache TTL is configurable via `MONITOR_CACHE_TTL`.

### Base URL

```
http://<host>:8082/monitor/v1
```

---

### GET /monitor/v1/runs

Returns a summary list of all WeaveRuns in the managed namespace.

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/monitor/v1/runs
```

```json
[
  {
    "name": "etl-pipeline-run-1",
    "chain": "etl-pipeline",
    "phase": "Succeeded",
    "startTime": "2026-04-12T02:00:05Z",
    "completionTime": "2026-04-12T02:14:32Z",
    "stepCount": 3,
    "failedSteps": 0,
    "message": ""
  }
]
```

---

### GET /monitor/v1/runs/{name}

Returns a detailed view: the full WeaveRun object, all associated batch/v1 Jobs, and Kubernetes Events for the run.

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/monitor/v1/runs/etl-pipeline-run-1
```

```json
{
  "run": { ... },
  "jobs": [ { ... } ],
  "events": [ { ... } ]
}
```

---

### GET /monitor/v1/runs/{name}/jobs

Returns all `batch/v1 Jobs` labelled `fusion-platform.io/run=<name>`.

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8082/monitor/v1/runs/etl-pipeline-run-1/jobs
```

---

### GET /monitor/v1/runs/{name}/jobs/{jobName}

Returns a single batch/v1 Job by name.

```bash
curl -H "Authorization: Bearer $KEY" \
  http://localhost:8082/monitor/v1/runs/etl-pipeline-run-1/jobs/etl-pipeline-run-1-extract-0
```

---

### GET /monitor/v1/runs/{name}/steps/{step}/logs

Returns a JSON snapshot of the last N log lines from the most recently created pod for the named step.

```bash
curl -H "Authorization: Bearer $KEY" \
  http://localhost:8082/monitor/v1/runs/etl-pipeline-run-1/steps/extract/logs
```

```json
{
  "runName": "etl-pipeline-run-1",
  "stepName": "extract",
  "podName": "etl-pipeline-run-1-extract-0-abcde",
  "lines": [
    "Extracting records from source...",
    "{\"records\": 1000, \"source\": \"db\"}"
  ]
}
```

`N` is configurable via `MONITOR_LOG_LINES` (default `100`). The response is cached; log lines are also published to the configured log sink asynchronously.

---

### GET /monitor/v1/runs/{name}/events

Returns all Kubernetes `core/v1 Events` whose `involvedObject.name` matches the run name and `involvedObject.kind=WeaveRun`.

```bash
curl -H "Authorization: Bearer $KEY" \
  http://localhost:8082/monitor/v1/runs/etl-pipeline-run-1/events
```

---

### GET /monitor/v1/events

Returns all Kubernetes Events in the managed namespace. Accepts an optional `?fieldSelector=` query parameter forwarded to the Kubernetes API.

```bash
# All events
curl -H "Authorization: Bearer $KEY" http://localhost:8082/monitor/v1/events

# Filter by reason
curl -H "Authorization: Bearer $KEY" \
  "http://localhost:8082/monitor/v1/events?fieldSelector=reason=BackOff"
```

The `fieldSelector` value is validated against an allowlist regex (`[a-zA-Z0-9./=!,_()\- ]{0,512}`) before use.

---

### GET /monitor/v1/chains/{name}/deployments

Returns all `apps/v1 Deployments` labelled `fusion-platform.io/chain=<name>`.

```bash
curl -H "Authorization: Bearer $KEY" \
  http://localhost:8082/monitor/v1/chains/ci-demo/deployments
```

---

### GET /monitor/v1/stats/runs

Returns aggregated statistics across all WeaveRuns within the specified time window.

**Query parameters:**

| Parameter | Default | Description |
|---|---|---|
| `window` | `1h` | Time window. Accepts Go duration strings (`30m`, `2h`) or day suffix (`7d`). |

```bash
curl -H "Authorization: Bearer $KEY" \
  "http://localhost:8082/monitor/v1/stats/runs?window=24h"
```

```json
{
  "window": "24h",
  "total": 12,
  "succeeded": 10,
  "failed": 1,
  "running": 1,
  "pending": 0,
  "stopped": 0,
  "successRate": 0.9090909090909091,
  "avgDurationMs": 184200,
  "minDurationMs": 94000,
  "maxDurationMs": 421000
}
```

**Window semantics:**
- Completed runs: included if `startTime` or `completionTime` falls within the window.
- Active runs: included only if `startTime` falls within the window (prevents old stuck runs from skewing stats).
- `successRate` = `succeeded / (succeeded + failed + stopped)`.
- Duration fields are `0` when no completed runs exist in the window.

---

### GET /monitor/v1/stats/chains/{name}

Same as `/monitor/v1/stats/runs` but scoped to runs that belong to the named WeaveChain (filtered in-process by `spec.chainRef.name`).

```bash
curl -H "Authorization: Bearer $KEY" \
  "http://localhost:8082/monitor/v1/stats/chains/etl-pipeline?window=7d"
```
