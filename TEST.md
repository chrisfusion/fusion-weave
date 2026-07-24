# Testing Guide

Two layers: fast package-level `go test` unit tests (no cluster required, run
these first) and an end-to-end manual test playbook for the fusion-weave REST
API (CRUD) and Monitoring API on a local minikube cluster.

---

## Table of Contents

0. [Unit tests (go test)](#0-unit-tests-go-test)
1. [Environment setup](#1-environment-setup)
2. [Build and deploy](#2-build-and-deploy)
3. [Enable monitoring API](#3-enable-monitoring-api)
4. [Port-forward](#4-port-forward)
5. [CRUD API — WeaveJobTemplate](#5-crud-api--weavejobtemplate)
6. [CRUD API — WeaveServiceTemplate](#6-crud-api--weaveservicetemplate)
7. [CRUD API — WeaveChain](#7-crud-api--weavechain)
8. [CRUD API — WeaveTrigger](#8-crud-api--weavetrigger)
9. [CRUD API — WeaveRun (manual)](#9-crud-api--weaverun-manual)
10. [Fire a run and watch it complete](#10-fire-a-run-and-watch-it-complete)
11. [Monitoring API — runs](#11-monitoring-api--runs)
12. [Monitoring API — jobs](#12-monitoring-api--jobs)
13. [Monitoring API — pod logs](#13-monitoring-api--pod-logs)
14. [Monitoring API — events](#14-monitoring-api--events)
15. [Monitoring API — deployments](#15-monitoring-api--deployments)
16. [Monitoring API — statistics](#16-monitoring-api--statistics)
17. [Prometheus metrics](#17-prometheus-metrics)
18. [Authentication](#18-authentication)
19. [Error cases](#19-error-cases)
20. [Cleanup](#20-cleanup)

---

## 0. Unit tests (go test)

No cluster needed — run this before touching minikube at all.

```bash
make test
# equivalent to: go test ./... -v
```

### Package coverage

| Package | Test files | What's exercised |
|---|---|---|
| `internal/dag` | `graph_test.go` | Pure-Go DAG engine (graph building, cycle detection, advance/executor logic) — no Kubernetes dependency. |
| `internal/deploybuilder` | `builder_test.go` | `Build` (chain-owned) and `BuildFromOverride` (run-owned) construction of apps/v1 Deployment + Service + Ingress from `WeaveServiceTemplate`/`WeaveRunStepOverride` + `indexclient` metadata. |
| `internal/jobbuilder` | `builder_test.go` | `WeaveJobTemplate` + `WeaveChainStep` + `WeaveRun` → batch/v1 Job spec translation. |
| `internal/indexclient` | `client_test.go`, `fetch_test.go` | `parseMetadata` (YAML→struct) and `FetchAppMetadataAndVersion`/`FetchAppMetadata`/`ResolveTag` HTTP calls against a fusion-index stand-in. |
| `internal/backup` | `dump_test.go`, `restore_test.go` | `DumpObjects`/`RestoreObjects`/`HasExistingObjects` against an in-memory fake Kubernetes client — see below, this is the reference pattern for any future controller-adjacent unit test. |

**Packages with no unit tests today** — their only verification is the
minikube e2e playbook in sections 1+ below: `internal/controller` (all 5
reconcilers, including the WeaveRun execution engine), `internal/trigger`
(`CronScheduler`, `BatchCronScheduler`, `KafkaConsumer`, `webhook.go`,
`s3event.go`), `internal/codesource` (shared `WEAVE_*` env var / writable-path
volume-naming helpers used by both the chain controller and `cmd/loader`),
`internal/apiserver` (+ `auth`, `handlers`, `middleware`), `internal/monitoring`
(+ `cache`, `handlers`, `logsink`), `internal/security`, `cmd/loader`,
`cmd/backup`, `cmd/api`. If you add meaningful logic to any of these, prefer
adding a real unit test over extending only the manual playbook — e.g.
`internal/codesource`'s path-sanitization and env-var helpers are pure
functions and don't need a cluster; `internal/trigger`'s scheduler goroutines
could be tested with a fake fire-channel consumer the way `BatchCronScheduler`
already isolates schedule parsing from the min-heap goroutine.

### Conventions (see also the "## Testing" section of `CLAUDE.md`)

- **Package naming**: tests of unexported functions use `package <pkg>` (same
  package — e.g. `internal/backup`, and `internal/indexclient/client_test.go`
  for `parseMetadata`); tests that only exercise exported functions use
  `package <pkg>_test` (e.g. `internal/dag`, `internal/deploybuilder`,
  `internal/jobbuilder`). Match whatever the existing test file in that
  package already uses — don't mix both styles in one package.
- **Fake client pattern** (`internal/backup/*_test.go`) — the reference
  pattern for unit-testing anything that takes a `client.Client` without a
  real cluster:

  ```go
  scheme := runtime.NewScheme()
  clientgoscheme.AddToScheme(scheme)   // k8s core types
  weavev1alpha1.AddToScheme(scheme)    // WeaveJobTemplate, WeaveChain, ...
  c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(jt, chain).Build()
  ```

  `sigs.k8s.io/controller-runtime/pkg/client/fake` gives an in-memory
  `client.Client`. Register both `clientgoscheme` and `weavev1alpha1` on the
  scheme (`internal/backup/dump_test.go`'s `testScheme(t)` /
  `seededClient(t)` helpers are a ready-made template — copy them into a new
  package rather than reinventing). `internal/backup` never touches
  `WeaveRun` or any `.status` subresource, so the fake client's lack of a
  real status subresource split hasn't mattered yet; a reconciler test would
  need to account for that.
- **HTTP layer pattern** (`internal/indexclient/fetch_test.go`) — spin up
  `httptest.NewServer` over a Go 1.22+ `http.NewServeMux()` with
  method-qualified routes and path wildcards, mirroring the real fusion-index
  API shape:

  ```go
  mux := http.NewServeMux()
  mux.HandleFunc("GET /api/v1/artifacts", ...)
  mux.HandleFunc("GET /api/v1/artifacts/{id}/versions", ...)
  ts := httptest.NewServer(mux)
  ```

  Used for both `FetchAppMetadataAndVersion` happy-path and error-path tests
  (404s, malformed YAML, tag not found). The same pattern is the natural fit
  for any future `apiserver`/`monitoring` HTTP handler test.
- **`sigs.k8s.io/yaml` v1.4.0 gotcha**: a quoted numeric YAML string (e.g.
  `port: "8080"`) fails to parse into an `int32` field — YAML string → JSON
  string → `encoding/json` cannot unmarshal a string into a number. Unknown
  fields are silently ignored, not rejected. `internal/indexclient` and
  `internal/backup` both round-trip through this package; keep this in mind
  when hand-writing YAML fixtures in tests.

---

## 1. Environment setup

### Prerequisites

```bash
minikube start --cpus=4 --memory=4g
kubectl config use-context minikube
kubectl create namespace fusion 2>/dev/null || true
```

### Verify cluster is ready

```bash
kubectl get nodes
# NAME       STATUS   ROLES           AGE   VERSION
# minikube   Ready    control-plane   ...   v1.x.x
```

---

## 2. Build and deploy

### Build the image

```bash
eval $(minikube docker-env)
docker build -t fusion-weave-operator:latest .
```

### Install CRDs

```bash
kubectl apply -f config/crd/bases/
kubectl get crd | grep weave
# weavejobtemplates.weave.fusion-platform.io
# weaveservicetemplates.weave.fusion-platform.io
# weavechains.weave.fusion-platform.io
# weavetriggers.weave.fusion-platform.io
# weaveruns.weave.fusion-platform.io
```

### Deploy RBAC and workloads

```bash
# Operator RBAC
kubectl apply -f config/rbac/serviceaccount.yaml \
              -f config/rbac/role.yaml \
              -f config/rbac/rolebinding.yaml

# API server RBAC (includes monitoring rules)
kubectl apply -f config/rbac/api-serviceaccount.yaml \
              -f config/rbac/api-role.yaml \
              -f config/rbac/api-rolebinding.yaml \
              -f config/rbac/api-clusterrole.yaml \
              -f config/rbac/api-clusterrolebinding.yaml

# Workloads
kubectl apply -f config/manager/manager.yaml
kubectl apply -f config/manager/api-server.yaml
```

### Verify pods are running

```bash
kubectl get pods -n fusion
# NAME                                  READY   STATUS    RESTARTS
# fusion-weave-operator-xxxxx           1/1     Running   0
# fusion-weave-api-xxxxx                1/1     Running   0
```

---

## 3. Enable monitoring API

The monitoring API is off by default. Patch the deployment to enable it:

```bash
kubectl patch deployment fusion-weave-api -n fusion --type='json' -p='[
  {"op":"add","path":"/spec/template/spec/containers/0/env/-","value":{"name":"MONITORING_ENABLED","value":"true"}},
  {"op":"add","path":"/spec/template/spec/containers/0/env/-","value":{"name":"METRICS_ADDR","value":":9091"}},
  {"op":"add","path":"/spec/template/spec/containers/0/env/-","value":{"name":"MONITOR_CACHE_TTL","value":"30s"}},
  {"op":"add","path":"/spec/template/spec/containers/0/env/-","value":{"name":"MONITOR_LOG_LINES","value":"100"}}
]'

kubectl rollout restart deployment/fusion-weave-api -n fusion
kubectl rollout status  deployment/fusion-weave-api -n fusion
```

Verify both servers are logging:

```bash
kubectl logs deployment/fusion-weave-api -n fusion --tail=5
# ... INFO api fusion-weave API server starting {"addr": ":8082"}
# ... INFO metrics-server starting metrics server {"addr": ":9091"}
```

> **Helm alternative** — if using the Helm chart instead of raw YAML:
> ```bash
> helm upgrade fusion-weave deployment/fusion-weave/ \
>   --reuse-values \
>   --set api.monitoring.enabled=true \
>   --set api.monitoring.metricsPort=9091 \
>   --set api.monitoring.cacheTTL=30s \
>   --set api.monitoring.maxLogLines=100
> ```

---

## 4. Port-forward

Port-forward directly to the pod (the Service does not expose port 9091 unless installed via Helm with `api.monitoring.enabled=true`):

```bash
# Find the API pod name
API_POD=$(kubectl get pods -n fusion --no-headers | grep "fusion-weave-api" | awk '{print $1}' | head -1)
echo "API pod: $API_POD"

# Forward both ports in the background
kubectl port-forward pod/$API_POD 8082:8082 9091:9091 -n fusion &
PF_PID=$!

# Verify both are reachable
curl -s http://localhost:8082/healthz
# {"status":"ok"}
curl -s http://localhost:9091/metrics | head -3
# # HELP go_gc_duration_seconds ...
```

> To stop the port-forward later: `kill $PF_PID`

Set a convenience variable used throughout this guide:

```bash
# No auth in dev mode (ALLOW_UNAUTHENTICATED=true)
API=http://localhost:8082
# If auth is enabled, set: H="-H 'Authorization: Bearer <key>'"
H=""
```

---

## 5. CRUD API — WeaveJobTemplate

### Create

```bash
curl -s -X POST $API/api/v1/jobtemplates \
  -H "Content-Type: application/json" \
  $H \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveJobTemplate",
    "metadata": {"name": "test-echo", "namespace": "fusion"},
    "spec": {
      "image": "busybox:latest",
      "command": ["/bin/sh", "-c"],
      "args": ["echo \"hello from test-echo\""],
      "resources": {
        "requests": {"cpu": "50m", "memory": "32Mi"},
        "limits":   {"cpu": "100m", "memory": "64Mi"}
      }
    }
  }' | python3 -m json.tool
# Expected: 201, object with status.valid=true
```

### List

```bash
curl -s $API/api/v1/jobtemplates $H | python3 -c "
import sys, json
items = json.load(sys.stdin)['items']
for t in items:
    print(t['metadata']['name'], '  valid:', t['status'].get('valid'))"
# Expected: test-echo   valid: True  (plus any pre-existing templates)
```

### Get

```bash
curl -s $API/api/v1/jobtemplates/test-echo $H | python3 -m json.tool
# Expected: full object, status.valid=true
```

### Patch — change image

```bash
curl -s -X PATCH $API/api/v1/jobtemplates/test-echo \
  -H "Content-Type: application/merge-patch+json" \
  $H \
  -d '{"spec": {"image": "busybox:1.36"}}' | python3 -c "
import sys, json; t = json.load(sys.stdin)
print('image:', t['spec']['image'])"
# Expected: image: busybox:1.36
```

### Update — full replace

```bash
curl -s $API/api/v1/jobtemplates/test-echo $H | python3 -c "
import sys, json; t = json.load(sys.stdin)
t['spec']['args'] = ['echo \"updated args\"']
print(json.dumps(t))" > /tmp/test-echo-put.json

curl -s -X PUT $API/api/v1/jobtemplates/test-echo \
  -H "Content-Type: application/json" \
  $H \
  -d @/tmp/test-echo-put.json | python3 -c "
import sys, json; t = json.load(sys.stdin)
print('args:', t['spec']['args'])"
# Expected: args: ['echo "updated args"']
```

### Delete

```bash
curl -s -X DELETE $API/api/v1/jobtemplates/test-echo $H
# Expected: 200 OK, {"name":"test-echo"}
curl -s $API/api/v1/jobtemplates/test-echo $H | python3 -m json.tool
# Expected: {"code":404,"message":"resource not found"}
```

---

## 6. CRUD API — WeaveServiceTemplate

### Create

```bash
curl -s -X POST $API/api/v1/servicetemplates \
  -H "Content-Type: application/json" \
  $H \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveServiceTemplate",
    "metadata": {"name": "test-nginx", "namespace": "fusion"},
    "spec": {
      "image": "nginx:alpine",
      "replicas": 1,
      "ports": [{"name": "http", "port": 80, "targetPort": 80}],
      "readinessProbe": {
        "httpGet": {"path": "/", "port": 80},
        "initialDelaySeconds": 5,
        "periodSeconds": 10
      },
      "resources": {
        "requests": {"cpu": "50m", "memory": "32Mi"},
        "limits":   {"cpu": "200m", "memory": "128Mi"}
      },
      "serviceType": "ClusterIP",
      "unhealthyDuration": "5m"
    }
  }' | python3 -c "
import sys, json; t = json.load(sys.stdin)
print('created:', t['metadata']['name'], '  valid:', t['status'].get('valid'))"
# Expected: created: test-nginx   valid: True
```

### Patch — scale replicas

```bash
curl -s -X PATCH $API/api/v1/servicetemplates/test-nginx \
  -H "Content-Type: application/merge-patch+json" \
  $H \
  -d '{"spec": {"replicas": 2}}' | python3 -c "
import sys, json; print('replicas:', json.load(sys.stdin)['spec']['replicas'])"
# Expected: replicas: 2
```

### Delete

```bash
curl -s -X DELETE $API/api/v1/servicetemplates/test-nginx $H
# Expected: 200 OK
```

### codeSource smoke test (code-loader init container)

`spec.codeSource` injects a `code-loader` init container that resolves a
fusion-index artifact tag, downloads its files as-is into `codeSource.mountPath`
(default `/weave-code`), and writes a `.version` file. Requires a reachable
fusion-index with a published artifact (see `../fusion-testcases/testcases_v2/README.md`
for how to publish one, or enable a Tier 2 showroom app —
`showroom.codeSourceApps.*` in `deployment/fusion-weave/values.yaml`).

```bash
curl -s -X POST $API/api/v1/servicetemplates \
  -H "Content-Type: application/json" $H \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveServiceTemplate",
    "metadata": {"name": "test-codesource", "namespace": "fusion"},
    "spec": {
      "image": "fusion-weave-operator:latest",
      "replicas": 1,
      "ports": [{"name": "http", "port": 8080, "targetPort": 8080}],
      "codeSource": {
        "artifactName": "myapp",
        "tag": "stable",
        "loaderImage": "fusion-weave-operator:latest"
      },
      "resources": {"requests":{"cpu":"50m","memory":"32Mi"},"limits":{"cpu":"200m","memory":"128Mi"}},
      "serviceType": "ClusterIP"
    }
  }' | python3 -c "
import sys, json; t = json.load(sys.stdin)
print('created:', t['metadata']['name'], '  valid:', t['status'].get('valid'))"
```

After the owning WeaveChain fires a run with a Deploy-kind step referencing
this template, verify the init container ran and wrote the version file:

```bash
POD=$(kubectl get pods -n fusion -l fusion-platform.io/step=<stepName> -o jsonpath='{.items[0].metadata.name}')
kubectl logs $POD -n fusion -c code-loader
kubectl exec $POD -n fusion -c <containerName> -- cat /weave-code/.version
```

```bash
curl -s -X DELETE $API/api/v1/servicetemplates/test-codesource $H
```

---

## 7. CRUD API — WeaveChain

### Create a two-step chain with output passing

```bash
# First create the templates the chain references
curl -s -X POST $API/api/v1/jobtemplates \
  -H "Content-Type: application/json" $H \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveJobTemplate",
    "metadata": {"name": "test-producer", "namespace": "fusion"},
    "spec": {
      "image": "busybox:latest",
      "command": ["/bin/sh", "-c"],
      "args": ["echo '\''hello from test-echo'\''"],
      "resources": {"requests":{"cpu":"50m","memory":"32Mi"},"limits":{"cpu":"100m","memory":"64Mi"}}
    }
  }' > /dev/null

curl -s -X POST $API/api/v1/jobtemplates \
  -H "Content-Type: application/json" $H \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveJobTemplate",
    "metadata": {"name": "test-consumer", "namespace": "fusion"},
    "spec": {
      "image": "busybox:latest",
      "command": ["/bin/sh", "-c"],
      "args": ["echo \"consuming input\""],
      "resources": {"requests":{"cpu":"50m","memory":"32Mi"},"limits":{"cpu":"100m","memory":"64Mi"}}
    }
  }' > /dev/null

# Create the chain
curl -s -X POST $API/api/v1/chains \
  -H "Content-Type: application/json" \
  $H \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveChain",
    "metadata": {"name": "test-chain", "namespace": "fusion"},
    "spec": {
      "failurePolicy": "StopAll",
      "steps": [
        {
          "name": "produce",
          "stepKind": "Job",
          "jobTemplateRef": {"name": "test-producer"},
          "producesOutput": true
        },
        {
          "name": "consume",
          "stepKind": "Job",
          "jobTemplateRef": {"name": "test-consumer"},
          "dependsOn": ["produce"],
          "consumesOutputFrom": ["produce"]
        }
      ]
    }
  }' | python3 -c "
import sys, json; c = json.load(sys.stdin)
print('chain:', c['metadata']['name'], '  valid:', c['status'].get('valid'))"
# Expected: chain: test-chain   valid: True
```

### List

```bash
curl -s $API/api/v1/chains $H | python3 -c "
import sys, json
for c in json.load(sys.stdin)['items']:
    print(c['metadata']['name'], '  valid:', c['status'].get('valid'), '  steps:', len(c['spec']['steps']))"
```

### Patch — change failure policy

```bash
curl -s -X PATCH $API/api/v1/chains/test-chain \
  -H "Content-Type: application/merge-patch+json" \
  $H \
  -d '{"spec": {"failurePolicy": "ContinueOthers"}}' | python3 -c "
import sys, json; print('failurePolicy:', json.load(sys.stdin)['spec']['failurePolicy'])"
# Expected: failurePolicy: ContinueOthers
```

### Delete

```bash
curl -s -X DELETE $API/api/v1/chains/test-chain $H
curl -s -X DELETE $API/api/v1/jobtemplates/test-producer $H
curl -s -X DELETE $API/api/v1/jobtemplates/test-consumer $H
```

---

## 8. CRUD API — WeaveTrigger

### Create a cron trigger

```bash
# Requires an existing chain — use deploy-demo (already installed)
curl -s -X POST $API/api/v1/triggers \
  -H "Content-Type: application/json" \
  $H \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveTrigger",
    "metadata": {"name": "test-cron", "namespace": "fusion"},
    "spec": {
      "chainRef": {"name": "deploy-demo"},
      "type": "Cron",
      "schedule": "0 0 3 * * *"
    }
  }' | python3 -c "
import sys, json; t = json.load(sys.stdin)
print('trigger:', t['metadata']['name'], '  type:', t['spec']['type'], '  schedule:', t['spec'].get('schedule'))"
# Expected: trigger: test-cron   type: Cron   schedule: 0 0 3 * * *
```

### Create an on-demand trigger

```bash
curl -s -X POST $API/api/v1/triggers \
  -H "Content-Type: application/json" \
  $H \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveTrigger",
    "metadata": {"name": "test-ondemand", "namespace": "fusion"},
    "spec": {
      "chainRef": {"name": "deploy-demo"},
      "type": "OnDemand"
    }
  }' | python3 -c "
import sys, json; t = json.load(sys.stdin)
print('trigger:', t['metadata']['name'], '  type:', t['spec']['type'])"
```

### Fire via PATCH annotation

```bash
curl -s -X PATCH $API/api/v1/triggers/test-ondemand \
  -H "Content-Type: application/merge-patch+json" \
  $H \
  -d '{"metadata": {"annotations": {"fusion-platform.io/fire": "true"}}}' \
  | python3 -c "
import sys, json; t = json.load(sys.stdin)
print('annotations:', t['metadata'].get('annotations', {}))"
# Expected: annotation set — run will be created within a few seconds
```

### Update cron schedule

```bash
curl -s -X PATCH $API/api/v1/triggers/test-cron \
  -H "Content-Type: application/merge-patch+json" \
  $H \
  -d '{"spec": {"schedule": "0 0 4 * * *"}}' | python3 -c "
import sys, json; print('new schedule:', json.load(sys.stdin)['spec']['schedule'])"
# Expected: new schedule: 0 0 4 * * *
```

### List and delete

```bash
curl -s $API/api/v1/triggers $H | python3 -c "
import sys, json
for t in json.load(sys.stdin)['items']:
    print(t['metadata']['name'], t['spec']['type'])"

curl -s -X DELETE $API/api/v1/triggers/test-cron $H
curl -s -X DELETE $API/api/v1/triggers/test-ondemand $H
```

### BatchCron trigger (internal/trigger BatchCronScheduler)

Fires individual jobs read from a YAML job list in a ConfigMap (key
`jobs.yaml`), each entry carrying its own 5-field standard cron schedule (no
seconds field — different from the 6-field seconds-first `spec.schedule` used
by `Cron` triggers above, don't copy one format into the other). The showroom
ships a ready fixture (`trigger-batchcron.yaml`, gated by
`showroom.chains.triggerBatchCron`) if you'd rather enable that than hand-roll
a ConfigMap.

```bash
kubectl create configmap test-batchcron-jobs -n fusion --from-literal=jobs.yaml='
- name: hello
  schedule: "*/5 * * * *"
  chainRef: deploy-demo
'

curl -s -X POST $API/api/v1/triggers \
  -H "Content-Type: application/json" $H \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveTrigger",
    "metadata": {"name": "test-batchcron", "namespace": "fusion"},
    "spec": {
      "chainRef": {"name": "deploy-demo"},
      "type": "BatchCron",
      "batchCron": {"jobsConfigMapRef": {"name": "test-batchcron-jobs"}}
    }
  }' | python3 -c "
import sys, json; t = json.load(sys.stdin)
print('trigger:', t['metadata']['name'], '  type:', t['spec']['type'])"

# status.batchJobCount / batchJobErrors reflect how many entries parsed
kubectl get weavetrigger test-batchcron -n fusion \
  -o jsonpath='{.status.batchJobCount} valid, {.status.batchJobErrors} errors{"\n"}'

curl -s -X DELETE $API/api/v1/triggers/test-batchcron $H
kubectl delete configmap test-batchcron-jobs -n fusion
```

### Kafka trigger (internal/trigger KafkaConsumer)

Requires a reachable Kafka broker — point at a local Redpanda instance (see
`deployment/local-dev/redpanda-values.yaml`, bootstrap address
`redpanda-0.redpanda.redpanda.svc.cluster.local:9093`) or enable
`showroom.chains.triggerKafka` (defaults to `false` precisely because it needs
a real broker) with `showroom.kafka.brokers`/`topic` pointed at it.

```bash
curl -s -X POST $API/api/v1/triggers \
  -H "Content-Type: application/json" $H \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveTrigger",
    "metadata": {"name": "test-kafka", "namespace": "fusion"},
    "spec": {
      "chainRef": {"name": "deploy-demo"},
      "type": "Kafka",
      "kafka": {
        "brokers": ["redpanda-0.redpanda.redpanda.svc.cluster.local:9093"],
        "topic": "s3-events",
        "consumerGroup": "fusion-weave-test"
      }
    }
  }' | python3 -c "
import sys, json; t = json.load(sys.stdin)
print('trigger:', t['metadata']['name'], '  type:', t['spec']['type'])"

# Publish a test S3-event-shaped message to the topic, then confirm a run appears
kubectl get weaveruns -n fusion --sort-by=.metadata.creationTimestamp \
  -o jsonpath='{.items[-1].metadata.name} {.items[-1].spec.triggerRef.name}{"\n"}'

curl -s -X DELETE $API/api/v1/triggers/test-kafka $H
```

---

## 9. CRUD API — WeaveRun (manual)

### Create a manual run

```bash
curl -s -X POST $API/api/v1/runs \
  -H "Content-Type: application/json" \
  $H \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveRun",
    "metadata": {"name": "manual-test-run", "namespace": "fusion"},
    "spec": {
      "chainRef": {"name": "deploy-demo"},
      "parameterOverrides": [
        {"name": "TEST_MODE", "value": "manual"}
      ]
    }
  }' | python3 -c "
import sys, json; r = json.load(sys.stdin)
print('run:', r['metadata']['name'], '  phase:', r['status'].get('phase', 'Pending'))"
# Expected: run: manual-test-run   phase: Pending (or Running)
```

### Create a run with stepOverrides (run-owned deploy step)

`spec.stepOverrides` makes a Deploy-kind step run-owned instead of
chain-owned: the operator reads runner config from the fusion-index
artifact's `metadata.yaml` instead of the `WeaveServiceTemplate`, and names
the Deployment `<runName>-<stepName>` so multiple runs on the same chain
don't collide. The target chain must already have a Deploy-kind step named
`stepName` referencing a `WeaveServiceTemplate`.

```bash
curl -s -X POST $API/api/v1/runs \
  -H "Content-Type: application/json" $H \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveRun",
    "metadata": {"name": "override-test-run", "namespace": "fusion"},
    "spec": {
      "chainRef": {"name": "deploy-demo"},
      "stepOverrides": [
        {"stepName": "deploy", "artifactName": "myapp", "tag": "stable", "ingressName": "myapp-override"}
      ]
    }
  }' | python3 -c "
import sys, json; r = json.load(sys.stdin)
print('run:', r['metadata']['name'], '  phase:', r['status'].get('phase', 'Pending'))"

# Confirm the run-owned Deployment (not the chain-owned one) came up
kubectl get deployment override-test-run-deploy -n fusion \
  -o jsonpath='{.status.availableReplicas}/{.spec.replicas} available{"\n"}'

# Deleting the run tears down the override Deployment/Service/Ingress
# (the deploy-cleanup finalizer) — the chain-owned Deployment is untouched.
curl -s -X DELETE $API/api/v1/runs/override-test-run $H
```

### Poll until terminal via CRUD API

```bash
RUN=manual-test-run
for i in $(seq 1 20); do
  PHASE=$(curl -s $API/api/v1/runs/$RUN $H | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',{}).get('phase','?'))")
  echo "[$i] phase: $PHASE"
  case "$PHASE" in Succeeded|Failed|Stopped) break;; esac
  sleep 4
done
```

### List runs and filter

```bash
# All runs
curl -s $API/api/v1/runs $H | python3 -c "
import sys, json
for r in json.load(sys.stdin)['items']:
    start = r['status'].get('startTime','?')[:19]
    print(f\"{r['metadata']['name']:<40} {r['status'].get('phase','?'):<12} started={start}\")"

# Filter succeeded runs with jq (if installed)
curl -s $API/api/v1/runs $H | python3 -c "
import sys, json
items = [r for r in json.load(sys.stdin)['items'] if r['status'].get('phase') == 'Succeeded']
print(f'{len(items)} succeeded runs')"
```

### Add a label via PATCH

```bash
curl -s -X PATCH $API/api/v1/runs/manual-test-run \
  -H "Content-Type: application/merge-patch+json" \
  $H \
  -d '{"metadata": {"labels": {"test": "manual"}}}' | python3 -c "
import sys, json; print('labels:', json.load(sys.stdin)['metadata'].get('labels'))"
```

### Delete a run

```bash
curl -s -X DELETE $API/api/v1/runs/manual-test-run $H
# Associated Jobs and PVC (if any) are GC'd via owner references
```

---

## 10. Fire a run and watch it complete

This section exercises the full operator pipeline using the pre-installed `deploy-demo` chain.

```bash
# Fire via trigger annotation (kubectl)
kubectl annotate weavetrigger deploy-demo-trigger \
  fusion-platform.io/fire=true -n fusion --overwrite

# Or fire via REST API
curl -s -X PATCH $API/api/v1/triggers/deploy-demo-trigger \
  -H "Content-Type: application/merge-patch+json" $H \
  -d '{"metadata": {"annotations": {"fusion-platform.io/fire": "true"}}}'

# Get the name of the newly created run
sleep 2
RUN=$(kubectl get weaveruns -n fusion \
  --sort-by=.metadata.creationTimestamp \
  -o jsonpath='{.items[-1].metadata.name}')
echo "Run: $RUN"

# Watch job pods appear
kubectl get pods -n fusion -w --field-selector="status.phase!=Succeeded" &
WATCH_PID=$!

# Poll until done
for i in $(seq 1 30); do
  STATUS=$(kubectl get weaverun $RUN -n fusion \
    -o jsonpath='{.status.phase} steps: {range .status.steps[*]}{.name}={.phase} {end}')
  echo "[$i] $STATUS"
  case "$(echo $STATUS | awk '{print $1}')" in Succeeded|Failed|Stopped) break;; esac
  sleep 3
done
kill $WATCH_PID 2>/dev/null

# Confirm all resources
kubectl get jobs -n fusion | grep $RUN
kubectl get pods -n fusion | grep $RUN
```

---

## 11. Monitoring API — runs

Use a run that already completed (replace `deploy-demo-trigger-62nzq` with an actual run name from `kubectl get fr -n fusion`):

```bash
RUN=$(kubectl get weaveruns -n fusion \
  --sort-by=.metadata.creationTimestamp \
  -o jsonpath='{.items[0].metadata.name}')
echo "Testing with run: $RUN"
```

### List all run summaries

```bash
curl -s $API/monitor/v1/runs | python3 -m json.tool
# Expected: array of {name, chain, phase, startTime, completionTime, stepCount, failedSteps}
```

### Get run detail (run + jobs + events)

```bash
curl -s $API/monitor/v1/runs/$RUN | python3 -c "
import sys, json
d = json.load(sys.stdin)
r = d['run']['status']
print('phase:', r['phase'])
print('steps:')
for s in r.get('steps', []):
    print(f\"  {s['name']:<20} phase={s['phase']}\")
print('jobs:', len(d.get('jobs', [])))
print('events:', len(d.get('events', [])))"
# Expected: phase Succeeded, all steps, jobs count, events count
```

### Verify cache: second call is a cache hit

```bash
# Call once (miss)
curl -s $API/monitor/v1/runs/$RUN > /dev/null
H1=$(curl -s http://localhost:9091/metrics | grep '^weave_monitor_cache_hits_total' | awk '{print $2}')

# Call again immediately (hit)
curl -s $API/monitor/v1/runs/$RUN > /dev/null
H2=$(curl -s http://localhost:9091/metrics | grep '^weave_monitor_cache_hits_total' | awk '{print $2}')

echo "hits before=$H1  after=$H2"
[ "$H2" -gt "$H1" ] && echo "PASS: cache hit confirmed" || echo "FAIL: no hit recorded"
```

---

## 12. Monitoring API — jobs

```bash
### List jobs for a run
curl -s $API/monitor/v1/runs/$RUN/jobs | python3 -c "
import sys, json
jobs = json.load(sys.stdin)
for j in jobs:
    conds = [(c['type'], c['status']) for c in j['status'].get('conditions', [])]
    print(j['metadata']['name'], conds)"
# Expected: job names with Complete=True conditions

### Get a single job by name
JOB=$(curl -s $API/monitor/v1/runs/$RUN/jobs | python3 -c "
import sys, json; print(json.load(sys.stdin)[0]['metadata']['name'])")
echo "Job: $JOB"

curl -s $API/monitor/v1/runs/$RUN/jobs/$JOB | python3 -c "
import sys, json
j = json.load(sys.stdin)
print('name:', j['metadata']['name'])
print('succeeded:', j['status'].get('succeeded', 0))
print('startTime:', j['status'].get('startTime', '?'))"
# Expected: name=..., succeeded=1

### 404 for unknown job
curl -s $API/monitor/v1/runs/$RUN/jobs/does-not-exist | python3 -m json.tool
# Expected: {"code":404,"message":"resource not found"}
```

---

## 13. Monitoring API — pod logs

```bash
### Get log snapshot for a step
STEP=$(curl -s $API/monitor/v1/runs/$RUN/jobs | python3 -c "
import sys, json
jobs = json.load(sys.stdin)
# derive step name from job name (remove run prefix and retry suffix)
name = jobs[0]['metadata']['name']
# e.g. 'deploy-demo-trigger-62nzq-build-0' → step = 'build'
parts = name.split('-')
print(parts[-2])")
echo "Step: $STEP"

curl -s $API/monitor/v1/runs/$RUN/steps/$STEP/logs | python3 -c "
import sys, json
r = json.load(sys.stdin)
print('run:     ', r['runName'])
print('step:    ', r['stepName'])
print('pod:     ', r['podName'])
print('lines:')
for l in r['lines']: print(' ', l)"
# Expected: log lines from the pod

### 404 for unknown step
curl -s $API/monitor/v1/runs/$RUN/steps/no-such-step/logs | python3 -m json.tool
# Expected: {"code":404,"message":"step not found or has no associated job"}

### Result is cached — verify no duplicate Kubernetes API calls
curl -s $API/monitor/v1/runs/$RUN/steps/$STEP/logs > /dev/null  # miss
curl -s $API/monitor/v1/runs/$RUN/steps/$STEP/logs > /dev/null  # hit
curl -s http://localhost:9091/metrics | grep weave_monitor_cache_hits
```

---

## 14. Monitoring API — events

```bash
### Events for a specific run (involvedObject filter)
curl -s $API/monitor/v1/runs/$RUN/events | python3 -c "
import sys, json
evs = json.load(sys.stdin)
print(f'{len(evs)} events')
for e in evs[:5]:
    print(' ', e.get('reason'), '-', e.get('message','')[:60])"
# Expected: list of events (may be empty for fast successful runs)

### All events in the namespace
curl -s $API/monitor/v1/events | python3 -c "
import sys, json; print(len(json.load(sys.stdin)), 'total events')"
# Expected: positive number

### Filter by fieldSelector — only Warning events
curl -s '$API/monitor/v1/events?fieldSelector=type%3DWarning' | python3 -c "
import sys, json
evs = json.load(sys.stdin)
print(f'{len(evs)} Warning events')
for e in evs[:3]:
    print(' ', e.get('reason'), '-', e.get('message','')[:60])"

### Filter by reason
curl -s '$API/monitor/v1/events?fieldSelector=reason%3DBackOff' | python3 -c "
import sys, json; print(len(json.load(sys.stdin)), 'BackOff events')"

### Invalid fieldSelector — should return 400
curl -s '$API/monitor/v1/events?fieldSelector=foo%3Dbar%00injected' | python3 -m json.tool
# Expected: {"code":400,"message":"invalid fieldSelector"}

### fieldSelector too long (>512 chars) — should return 400
LONG=$(python3 -c "print('a'*513)")
curl -s "$API/monitor/v1/events?fieldSelector=$LONG" | python3 -m json.tool
# Expected: {"code":400,"message":"invalid fieldSelector"}
```

---

## 15. Monitoring API — deployments

This requires at least one Deploy-kind step to have run (the `deploy-demo` chain has one).

```bash
### List deployments owned by a chain
curl -s $API/monitor/v1/chains/deploy-demo/deployments | python3 -c "
import sys, json
deps = json.load(sys.stdin)
for d in deps:
    avail = next((c['status'] for c in d['status'].get('conditions',[]) if c['type']=='Available'), '?')
    ready = d['status'].get('readyReplicas', 0)
    desired = d['spec']['replicas']
    print(f\"{d['metadata']['name']:<30} available={avail}  replicas={ready}/{desired}\")"
# Expected: deploy-demo-deploy   available=True   replicas=2/2

### 404 for chain with no deployments
curl -s $API/monitor/v1/chains/no-such-chain/deployments | python3 -m json.tool
# Expected: [] (empty array — not 404, chain may not exist but query succeeds with empty result)
```

---

## 16. Monitoring API — statistics

### Global run stats

```bash
### Default window (1h) — runs older than 1h return zero counts
curl -s $API/monitor/v1/stats/runs | python3 -m json.tool
# Expected: window=1h, all counts 0 if no recent runs

### 24-hour window
curl -s '$API/monitor/v1/stats/runs?window=24h' | python3 -m json.tool
# Expected: total > 0, successRate between 0 and 1

### 7-day window
curl -s '$API/monitor/v1/stats/runs?window=7d' | python3 -m json.tool

### 30-minute window
curl -s '$API/monitor/v1/stats/runs?window=30m' | python3 -m json.tool

### Verify fields
curl -s '$API/monitor/v1/stats/runs?window=24h' | python3 -c "
import sys, json
s = json.load(sys.stdin)
assert 'window' in s
assert 'total' in s
assert 'succeeded' in s
assert 'failed' in s
assert 'running' in s
assert 'pending' in s
assert 'stopped' in s
assert 'successRate' in s
assert 'avgDurationMs' in s
assert 'minDurationMs' in s
assert 'maxDurationMs' in s
total = s['succeeded'] + s['failed'] + s['running'] + s['pending'] + s['stopped']
assert total == s['total'], f'counts do not sum: {total} != {s[\"total\"]}'
if s[\"total\"] > 0 and (s[\"succeeded\"] + s[\"failed\"] + s[\"stopped\"]) > 0:
    expected_rate = s[\"succeeded\"] / (s[\"succeeded\"] + s[\"failed\"] + s[\"stopped\"])
    assert abs(s[\"successRate\"] - expected_rate) < 0.001, \"successRate mismatch\"
print('PASS: all stat fields present and consistent')"
```

### Per-chain stats

```bash
### Stats scoped to deploy-demo chain
curl -s '$API/monitor/v1/stats/chains/deploy-demo?window=24h' | python3 -m json.tool
# Expected: same shape as global stats but only deploy-demo runs

### Cross-check: chain total <= global total
GLOBAL=$(curl -s '$API/monitor/v1/stats/runs?window=24h' | python3 -c "import sys,json; print(json.load(sys.stdin)['total'])")
CHAIN=$(curl -s '$API/monitor/v1/stats/chains/deploy-demo?window=24h' | python3 -c "import sys,json; print(json.load(sys.stdin)['total'])")
echo "global=$GLOBAL  chain=deploy-demo=$CHAIN"
python3 -c "assert $CHAIN <= $GLOBAL, 'chain count exceeds global'; print('PASS: chain count <= global count')"

### Unknown chain returns zeros (not an error)
curl -s '$API/monitor/v1/stats/chains/no-such-chain?window=24h' | python3 -c "
import sys, json; s = json.load(sys.stdin)
assert s['total'] == 0
print('PASS: unknown chain returns zeroed stats')"
```

---

## 17. Prometheus metrics

```bash
### Scrape all metrics
curl -s http://localhost:9091/metrics | grep -E '^(weave_|go_|process_)' | head -30

### Verify all weave_ metrics are present
curl -s http://localhost:9091/metrics | grep '^weave_'
# Expected output includes:
#   weave_monitor_cache_hits_total
#   weave_monitor_cache_misses_total
#   weave_monitor_request_duration_seconds_*
#   weave_monitor_requests_total
#   weave_runs_by_phase{phase="Failed"}
#   weave_runs_by_phase{phase="Pending"}
#   weave_runs_by_phase{phase="Running"}
#   weave_runs_by_phase{phase="Stopped"}
#   weave_runs_by_phase{phase="Succeeded"}

### Verify cache hit counter increments
HITS_BEFORE=$(curl -s http://localhost:9091/metrics | grep '^weave_monitor_cache_hits_total' | awk '{print $2}')
curl -s $API/monitor/v1/runs > /dev/null   # first call — miss
curl -s $API/monitor/v1/runs > /dev/null   # second call — hit
HITS_AFTER=$(curl -s http://localhost:9091/metrics | grep '^weave_monitor_cache_hits_total' | awk '{print $2}')
echo "hits: $HITS_BEFORE → $HITS_AFTER"
python3 -c "assert $HITS_AFTER > $HITS_BEFORE, 'expected hit counter to increase'; print('PASS')"

### Verify run phase gauges reflect current runs
curl -s $API/monitor/v1/runs > /dev/null   # refresh gauge
curl -s http://localhost:9091/metrics | grep 'weave_runs_by_phase'
# Expected: Succeeded > 0, Running = 0 (if no active runs)

### Metrics port has no auth — verify 200 without credentials
curl -v http://localhost:9091/metrics 2>&1 | grep "< HTTP"
# Expected: HTTP/1.1 200 OK

### Metrics NOT served on API port
curl -s http://localhost:8082/metrics | python3 -m json.tool
# Expected: {"code":404,...} or connection refused on /metrics path
```

---

## 18. Authentication

These tests require auth to be configured. Skip if running with `ALLOW_UNAUTHENTICATED=true`.

### Set up an API key

```bash
KEY=$(openssl rand -hex 32)
kubectl create secret generic test-api-key \
  --from-literal=key="$KEY" \
  --namespace=fusion
kubectl label   secret test-api-key fusion-platform.io/api-key=true -n fusion
kubectl annotate secret test-api-key fusion-platform.io/role=viewer   -n fusion

# Enable API key auth
kubectl set env deployment/fusion-weave-api -n fusion \
  ALLOW_UNAUTHENTICATED=false AUTH_APIKEY=true
kubectl rollout restart deployment/fusion-weave-api -n fusion
kubectl rollout status  deployment/fusion-weave-api -n fusion

# Re-establish port-forward after restart
API_POD=$(kubectl get pods -n fusion --no-headers | grep fusion-weave-api | awk '{print $1}' | head -1)
kill $PF_PID 2>/dev/null
kubectl port-forward pod/$API_POD 8082:8082 9091:9091 -n fusion &
PF_PID=$!
sleep 2
```

### Missing credentials → 401

```bash
curl -s $API/api/v1/chains | python3 -m json.tool
# Expected: {"code":401,"message":"unauthorized"}
```

### Valid viewer key → 200 on GET

```bash
curl -s -H "Authorization: Bearer $KEY" $API/api/v1/chains | python3 -c "
import sys, json; items = json.load(sys.stdin)['items']; print(len(items), 'chains')"
# Expected: list of chains
```

### Viewer key → 403 on DELETE

```bash
curl -s -X DELETE -H "Authorization: Bearer $KEY" \
  $API/api/v1/chains/deploy-demo | python3 -m json.tool
# Expected: {"code":403,"message":"forbidden"}
```

### Wrong token → 401

```bash
curl -s -H "Authorization: Bearer wrong-token" $API/api/v1/chains | python3 -m json.tool
# Expected: {"code":401,"message":"unauthorized"}
```

### Promote key to editor and test POST

```bash
kubectl annotate secret test-api-key fusion-platform.io/role=editor --overwrite -n fusion
# Wait a few seconds for the auth cache to clear, then:
curl -s -X POST $API/api/v1/jobtemplates \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "apiVersion": "weave.fusion-platform.io/v1alpha1",
    "kind": "WeaveJobTemplate",
    "metadata": {"name": "auth-test", "namespace": "fusion"},
    "spec": {"image": "busybox:latest", "command": ["echo"],
             "resources": {"requests":{"cpu":"50m","memory":"32Mi"},"limits":{"cpu":"100m","memory":"64Mi"}}}
  }' | python3 -c "import sys,json; print('created:', json.load(sys.stdin)['metadata']['name'])"
# Expected: created: auth-test

curl -s -X DELETE -H "Authorization: Bearer $KEY" \
  $API/api/v1/jobtemplates/auth-test | python3 -m json.tool
# Expected: {"code":403,"message":"forbidden"} — editor cannot DELETE
```

### Health endpoints bypass auth

```bash
curl -s $API/healthz | python3 -m json.tool
curl -s $API/readyz  | python3 -m json.tool
# Expected: {"status":"ok"} — no Authorization header needed

### Monitoring endpoints respect auth
curl -s $API/monitor/v1/runs | python3 -m json.tool
# Expected: {"code":401,"message":"unauthorized"}

curl -s -H "Authorization: Bearer $KEY" $API/monitor/v1/runs | python3 -c "
import sys, json; print(len(json.load(sys.stdin)), 'runs')"
# Expected: count of runs (viewer can read monitoring)
```

### Cleanup auth test

```bash
kubectl delete secret test-api-key -n fusion
kubectl set env deployment/fusion-weave-api -n fusion \
  ALLOW_UNAUTHENTICATED=true AUTH_APIKEY=false
kubectl rollout restart deployment/fusion-weave-api -n fusion
kubectl rollout status  deployment/fusion-weave-api -n fusion
# Re-forward
API_POD=$(kubectl get pods -n fusion --no-headers | grep fusion-weave-api | awk '{print $1}' | head -1)
kill $PF_PID 2>/dev/null
kubectl port-forward pod/$API_POD 8082:8082 9091:9091 -n fusion &
PF_PID=$!
sleep 2
```

---

## 19. Error cases

### CRUD API

```bash
### 404 — resource not found
curl -s $API/api/v1/jobtemplates/does-not-exist $H | python3 -m json.tool
# Expected: {"code":404,"message":"resource not found"}

### 400 — malformed JSON body
curl -s -X POST $API/api/v1/jobtemplates \
  -H "Content-Type: application/json" $H \
  -d 'not-valid-json' | python3 -m json.tool
# Expected: {"code":400,...}

### 400 — missing required field (image)
curl -s -X POST $API/api/v1/jobtemplates \
  -H "Content-Type: application/json" $H \
  -d '{"apiVersion":"weave.fusion-platform.io/v1alpha1","kind":"WeaveJobTemplate","metadata":{"name":"bad","namespace":"fusion"},"spec":{}}' \
  | python3 -m json.tool
# Expected: 400 or 422 — image is required

### 405 — method not allowed
curl -s -X DELETE $API/api/v1/jobtemplates $H | python3 -m json.tool
# Expected: {"code":405,...}

### Namespace mismatch — reject object in wrong namespace
curl -s -X POST $API/api/v1/jobtemplates \
  -H "Content-Type: application/json" $H \
  -d '{"apiVersion":"weave.fusion-platform.io/v1alpha1","kind":"WeaveJobTemplate","metadata":{"name":"cross-ns","namespace":"default"},"spec":{"image":"busybox","resources":{"requests":{"cpu":"50m","memory":"32Mi"},"limits":{"cpu":"100m","memory":"64Mi"}}}}' \
  | python3 -m json.tool
# Expected: error — cannot create in different namespace
```

### Monitoring API

```bash
### 404 — run not found
curl -s $API/monitor/v1/runs/no-such-run $H | python3 -m json.tool
# Expected: {"code":404,"message":"resource not found"}

### 404 — step not found
RUN=$(kubectl get fr -n fusion -o jsonpath='{.items[0].metadata.name}')
curl -s $API/monitor/v1/runs/$RUN/steps/no-such-step/logs $H | python3 -m json.tool
# Expected: {"code":404,"message":"step not found or has no associated job"}

### 400 — invalid fieldSelector (null byte injection)
curl -s '$API/monitor/v1/events?fieldSelector=reason%3DFoo%00evil' $H | python3 -m json.tool
# Expected: {"code":400,"message":"invalid fieldSelector"}

### 400 — fieldSelector exceeds 512 characters
LONG=$(python3 -c "print('a'*600)")
curl -s "$API/monitor/v1/events?fieldSelector=$LONG" $H | python3 -m json.tool
# Expected: {"code":400,"message":"invalid fieldSelector"}

### Stat window defaults to 1h on invalid value
curl -s '$API/monitor/v1/stats/runs?window=notaduration' $H | python3 -c "
import sys, json; s = json.load(sys.stdin); print('window:', s['window'])"
# Expected: window: notaduration (server uses 1h default but echoes the input string)

### Empty chain has no deployments (not an error)
curl -s $API/monitor/v1/chains/no-such-chain/deployments $H | python3 -c "
import sys, json; print('deployments:', json.load(sys.stdin))"
# Expected: [] (empty array)
```

---

## 20. Cleanup

```bash
# Kill port-forward
kill $PF_PID 2>/dev/null

# Remove test resources created during this session
kubectl delete weaverun manual-test-run override-test-run -n fusion 2>/dev/null || true
kubectl delete weavetrigger test-cron test-ondemand test-batchcron test-kafka -n fusion 2>/dev/null || true
kubectl delete weavechain test-chain -n fusion 2>/dev/null || true
kubectl delete weavejobtemplate test-echo test-producer test-consumer auth-test -n fusion 2>/dev/null || true
kubectl delete weaveservicetemplate test-nginx test-codesource -n fusion 2>/dev/null || true
kubectl delete configmap test-batchcron-jobs -n fusion 2>/dev/null || true

# Verify only pre-installed resources remain
kubectl get weavejobtemplate,weaveservicetemplate,weavechain,weavetrigger,weaverun -n fusion

# Full teardown (removes everything)
# kubectl delete namespace fusion
# helm uninstall fusion-weave 2>/dev/null || true
```

---

## Quick test sequence (5 minutes)

Paste this block to run a fast smoke test of the entire system:

```bash
API=http://localhost:8082

echo "--- health ---"
curl -s $API/healthz | python3 -c "import sys,json; r=json.load(sys.stdin); assert r['status']=='ok'; print('OK')"

echo "--- CRUD list ---"
curl -s $API/api/v1/chains   | python3 -c "import sys,json; print(len(json.load(sys.stdin)['items']), 'chains')"
curl -s $API/api/v1/runs     | python3 -c "import sys,json; print(len(json.load(sys.stdin)['items']), 'runs')"

echo "--- fire a run ---"
curl -s -X PATCH $API/api/v1/triggers/deploy-demo-trigger \
  -H "Content-Type: application/merge-patch+json" \
  -d '{"metadata":{"annotations":{"fusion-platform.io/fire":"true"}}}' > /dev/null
sleep 2
RUN=$(kubectl get fr -n fusion --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1].metadata.name}')
echo "run: $RUN"

echo "--- wait for completion ---"
kubectl wait weaverun $RUN -n fusion --for=jsonpath='{.status.phase}'=Succeeded --timeout=90s

echo "--- monitoring runs ---"
curl -s $API/monitor/v1/runs | python3 -c "import sys,json; print(len(json.load(sys.stdin)), 'run summaries')"

echo "--- monitoring detail ---"
curl -s $API/monitor/v1/runs/$RUN | python3 -c "
import sys, json; d=json.load(sys.stdin)
print('phase:', d['run']['status']['phase'])
print('jobs:', len(d['jobs']))"

echo "--- monitoring logs ---"
STEP=$(curl -s $API/monitor/v1/runs/$RUN/jobs | python3 -c "
import sys,json; j=json.load(sys.stdin)[0]['metadata']['name']; parts=j.split('-'); print(parts[-2])")
curl -s $API/monitor/v1/runs/$RUN/steps/$STEP/logs | python3 -c "
import sys,json; r=json.load(sys.stdin); print(len(r['lines']), 'log lines from pod', r['podName'])"

echo "--- monitoring stats ---"
curl -s '$API/monitor/v1/stats/runs?window=24h' | python3 -c "
import sys,json; s=json.load(sys.stdin)
print(f\"total={s['total']} succeeded={s['succeeded']} successRate={s['successRate']:.0%} avgDuration={s['avgDurationMs']}ms\")"

echo "--- prometheus ---"
curl -s http://localhost:9091/metrics | grep '^weave_' | wc -l | xargs echo "weave_ metric series:"

echo "--- ALL DONE ---"
```
