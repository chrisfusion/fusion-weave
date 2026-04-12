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
    "serviceAccountName": "my-sa"
  }
}
```

`image` is required. All other fields are optional.

`volumes` entries accept exactly one of `secretName` or `configMapName`:

```json
{"name": "creds", "mountPath": "/etc/creds", "secretName": "my-secret"}
```

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
          "host": "my-api.example.com",
          "path": "/",
          "pathType": "Prefix",
          "servicePort": 80
        }
      ],
      "tlsSecretName": "my-api-tls"
    },
    "unhealthyDuration": "5m",
    "revisionHistoryLimit": 3
  }
}
```

`image` and at least one entry in `ports` are required.

`serviceType` accepts `ClusterIP`, `NodePort`, or `LoadBalancer` (default `ClusterIP`).

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
    }
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
    "schedule": "0 2 * * *",
    "parameterOverrides": [
      {"name": "ENV", "value": "production"}
    ]
  }
}
```

**`type`**: `OnDemand`, `Cron`, or `Webhook`.

**Cron trigger** — requires `schedule` (standard cron expression):

```json
{
  "spec": {
    "chainRef": {"name": "etl-pipeline"},
    "type": "Cron",
    "schedule": "*/15 * * * *"
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
      "schedule": "0 2 * * *"
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
  -d '{"spec": {"schedule": "0 4 * * *"}}'
```

### Delete

```
DELETE /api/v1/triggers/{name}
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
    ]
  }
}
```

`chainRef` is required and immutable after creation.

`triggerRef` is optional — omit for manually created runs.

`parameterOverrides` values are injected as environment variables into each job pod, taking precedence over the template and chain defaults.

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
    ]
  }
}
```

**`phase`**: `Pending` → `Running` → `Succeeded` | `Failed` | `Stopped`

**`steps[].phase`**: `Pending`, `Running`, `Succeeded`, `Failed`, `Skipped`, `Retrying`

**`steps[].outputCaptured`**: `true` when the operator has captured JSON stdout from this step's job.

**`steps[].deploymentRef`**: set instead of `jobRef` for `stepKind: Deploy` steps.

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
| GET | `/api/v1/runs` | viewer | List all WeaveRuns |
| POST | `/api/v1/runs` | editor | Create a WeaveRun (manual trigger) |
| GET | `/api/v1/runs/{name}` | viewer | Get a WeaveRun |
| PUT | `/api/v1/runs/{name}` | editor | Full replace a WeaveRun |
| PATCH | `/api/v1/runs/{name}` | editor | Partial update a WeaveRun |
| DELETE | `/api/v1/runs/{name}` | admin | Delete a WeaveRun and its child resources |
