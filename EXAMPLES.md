# Examples

Practical recipes showing how to compose fusion-weave resources for real workloads.

**Quick reference:**
- [Hello World](#hello-world)
- [ETL pipeline — fan-out / fan-in](#etl-pipeline--fan-out--fan-in)
- [Output passing between steps](#output-passing-between-steps)
- [Shared storage](#shared-storage)
- [CI/CD pipeline — build, deploy, smoke test](#cicd-pipeline--build-deploy-smoke-test)
- [Deploy step with codeSource](#deploy-step-with-codesource)
- [Job step with codeSource](#job-step-with-codesource)
- [Multi-tenant service instances (stepOverrides)](#multi-tenant-service-instances-stepoverrides)
- [Shared auth secret — authSecretRef](#shared-auth-secret--authsecretref)
- [Failure branch / cleanup step](#failure-branch--cleanup-step)
- [Scheduled run](#scheduled-run)
- [On-demand run](#on-demand-run)
- [Webhook trigger](#webhook-trigger)
- [BatchCron trigger — job list from a ConfigMap](#batchcron-trigger--job-list-from-a-configmap)
- [Kafka trigger — S3 event consumption](#kafka-trigger--s3-event-consumption)
- [API key setup and usage](#api-key-setup-and-usage)
- [Monitoring API — observing runs](#monitoring-api--observing-runs)

---

## Hello World

The simplest possible chain: one job template, one step, one on-demand trigger.

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveJobTemplate
metadata:
  name: hello
  namespace: fusion
spec:
  image: busybox:latest
  command: ["/bin/sh", "-c"]
  args: ["echo 'Hello from fusion-weave!'"]
  resources:
    requests:
      cpu: 50m
      memory: 32Mi
    limits:
      cpu: 100m
      memory: 64Mi
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveChain
metadata:
  name: hello-chain
  namespace: fusion
spec:
  steps:
    - name: greet
      jobTemplateRef:
        name: hello
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveTrigger
metadata:
  name: hello-trigger
  namespace: fusion
spec:
  chainRef:
    name: hello-chain
  type: OnDemand
```

**Fire it:**
```bash
kubectl apply -f hello.yaml -n fusion
kubectl annotate weavetrigger hello-trigger fusion-platform.io/fire=true -n fusion
kubectl get fr -n fusion -w
kubectl logs -n fusion -l batch.kubernetes.io/job-name --tail=5
```

---

## ETL pipeline — fan-out / fan-in

`extract` → `transform-a` + `transform-b` (parallel) → `load`

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveJobTemplate
metadata:
  name: extractor
  namespace: fusion
spec:
  image: python:3.12-slim
  command: ["python", "-c"]
  args:
    - |
      import json, time
      print("Extracting records from source...")
      time.sleep(2)
      print(json.dumps({"records": 1000, "source": "db"}))
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 500m
      memory: 256Mi
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveJobTemplate
metadata:
  name: transformer
  namespace: fusion
spec:
  image: python:3.12-slim
  command: ["python", "-c"]
  args:
    - |
      import os, json
      partition = os.environ.get("PARTITION", "a")
      print(f"Transforming partition {partition}...")
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 500m
      memory: 256Mi
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveJobTemplate
metadata:
  name: loader
  namespace: fusion
spec:
  image: python:3.12-slim
  command: ["python", "-c"]
  args:
    - |
      print("Loading into data warehouse...")
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 500m
      memory: 256Mi
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveChain
metadata:
  name: etl-pipeline
  namespace: fusion
spec:
  failurePolicy: StopAll
  concurrencyPolicy: Wait
  steps:
    - name: extract
      jobTemplateRef:
        name: extractor

    - name: transform-a
      jobTemplateRef:
        name: transformer
      dependsOn: [extract]
      envOverrides:
        - name: PARTITION
          value: "a"

    - name: transform-b
      jobTemplateRef:
        name: transformer
      dependsOn: [extract]
      envOverrides:
        - name: PARTITION
          value: "b"

    - name: load
      jobTemplateRef:
        name: loader
      dependsOn: [transform-a, transform-b]
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveTrigger
metadata:
  name: etl-cron
  namespace: fusion
spec:
  chainRef:
    name: etl-pipeline
  type: Cron
  schedule: "0 0 2 * * *"   # daily at 02:00 (seconds-prefixed cron)
```

---

## Output passing between steps

Steps can produce JSON output and pass it to downstream consumers. The operator captures stdout from the producer and injects a merged JSON file at `/weave-input/input.json` in the consumer.

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveJobTemplate
metadata:
  name: data-fetcher
  namespace: fusion
spec:
  image: python:3.12-slim
  command: ["python", "-c"]
  args:
    - |
      import json
      # Anything printed to stdout is captured as this step's output.
      result = {"dataset": "sales-q1", "row_count": 42000, "checksum": "abc123"}
      print(json.dumps(result))
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 500m
      memory: 256Mi
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveJobTemplate
metadata:
  name: data-validator
  namespace: fusion
spec:
  image: python:3.12-slim
  command: ["python", "-c"]
  args:
    - |
      import json, sys

      # Input from upstream steps is available at /weave-input/input.json.
      # The JSON is namespaced by producer step name to avoid key collisions.
      with open("/weave-input/input.json") as f:
          inputs = json.load(f)

      fetch_result = inputs["fetch"]    # output from the "fetch" step
      print(f"Validating dataset: {fetch_result['dataset']}")
      print(f"Row count: {fetch_result['row_count']}")

      if fetch_result["row_count"] < 1:
          print("ERROR: empty dataset", file=sys.stderr)
          sys.exit(1)

      print(json.dumps({"valid": True, "dataset": fetch_result["dataset"]}))
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 500m
      memory: 256Mi
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveChain
metadata:
  name: validated-pipeline
  namespace: fusion
spec:
  steps:
    - name: fetch
      jobTemplateRef:
        name: data-fetcher
      producesOutput: true          # capture stdout as JSON output

    - name: validate
      jobTemplateRef:
        name: data-validator
      dependsOn: [fetch]
      consumesOutputFrom: [fetch]   # inject fetch's output into /weave-input/input.json
      producesOutput: true          # validate also produces output for downstream steps
```

> **Note:** `producesOutput: true` only captures the **last line** of stdout that is valid JSON. Non-JSON lines are ignored.

---

## Shared storage

All steps in the chain share a ReadWriteMany PVC mounted at `/weave-shared`. Useful when steps exchange large files that do not fit comfortably in JSON stdout.

> Requires a StorageClass that supports ReadWriteMany. On minikube: `minikube addons enable csi-hostpath-driver`

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveJobTemplate
metadata:
  name: file-writer
  namespace: fusion
spec:
  image: busybox:latest
  command: ["/bin/sh", "-c"]
  args:
    - |
      echo "Writing large artifact to shared volume..."
      dd if=/dev/urandom bs=1M count=10 of=/weave-shared/artifact.bin 2>/dev/null
      echo "artifact written: $(du -sh /weave-shared/artifact.bin)"
  resources:
    requests:
      cpu: 100m
      memory: 64Mi
    limits:
      cpu: 500m
      memory: 128Mi
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveJobTemplate
metadata:
  name: file-processor
  namespace: fusion
spec:
  image: busybox:latest
  command: ["/bin/sh", "-c"]
  args:
    - |
      echo "Processing artifact from shared volume..."
      ls -lh /weave-shared/
      wc -c /weave-shared/artifact.bin
      echo "Processing complete."
  resources:
    requests:
      cpu: 100m
      memory: 64Mi
    limits:
      cpu: 500m
      memory: 128Mi
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveChain
metadata:
  name: shared-storage-pipeline
  namespace: fusion
spec:
  # Provision a 500Mi RWX PVC — mounted at /weave-shared in every job pod.
  sharedStorage:
    size: "500Mi"
    storageClassName: "csi-hostpath-sc"   # must support ReadWriteMany
  steps:
    - name: write
      jobTemplateRef:
        name: file-writer

    - name: process
      jobTemplateRef:
        name: file-processor
      dependsOn: [write]
```

---

## CI/CD pipeline — build, deploy, smoke test

Combines a batch build job, a rolling-update deployment step, and a post-deploy smoke test — all in one chain.

```yaml
# ── Job templates ─────────────────────────────────────────────────────────────

apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveJobTemplate
metadata:
  name: ci-build
  namespace: fusion
spec:
  image: busybox:latest
  command: ["/bin/sh", "-c"]
  args:
    - |
      echo "Running tests..."
      sleep 3
      echo "Building container image..."
      sleep 2
      echo "Pushing to registry..."
      echo "Build complete. Image: myapp:$(date +%s)"
  resources:
    requests:
      cpu: 200m
      memory: 256Mi
    limits:
      cpu: 1000m
      memory: 512Mi
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveJobTemplate
metadata:
  name: ci-smoke-test
  namespace: fusion
spec:
  image: busybox:latest
  command: ["/bin/sh", "-c"]
  args:
    - |
      echo "Waiting for app to be ready..."
      sleep 5
      echo "Running smoke tests against http://ci-demo-app-deploy/"
      # wget -qO- http://ci-demo-app-deploy/healthz || exit 1
      echo "All smoke tests passed."
  resources:
    requests:
      cpu: 50m
      memory: 64Mi
    limits:
      cpu: 200m
      memory: 128Mi
---

# ── Service template (the deployed app) ───────────────────────────────────────

apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveServiceTemplate
metadata:
  name: myapp
  namespace: fusion
spec:
  image: nginx:alpine        # replace with your real app image
  replicas: 2
  ports:
    - name: http
      port: 80
      targetPort: 80
  readinessProbe:
    httpGet:
      path: /
      port: 80
    initialDelaySeconds: 5
    periodSeconds: 10
  resources:
    requests:
      cpu: 50m
      memory: 64Mi
    limits:
      cpu: 200m
      memory: 128Mi
  serviceType: ClusterIP
  unhealthyDuration: "5m"   # auto-rollback if not healthy within 5 minutes
  revisionHistoryLimit: 5
---

# ── Chain ─────────────────────────────────────────────────────────────────────

apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveChain
metadata:
  name: ci-demo
  namespace: fusion
spec:
  failurePolicy: StopAll
  steps:
    - name: build
      stepKind: Job
      jobTemplateRef:
        name: ci-build

    - name: app
      stepKind: Deploy
      serviceTemplateRef:
        name: myapp
      dependsOn: [build]

    - name: smoke-test
      stepKind: Job
      jobTemplateRef:
        name: ci-smoke-test
      dependsOn: [app]
---

# ── Webhook trigger (fire from your CI system) ─────────────────────────────────

apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveTrigger
metadata:
  name: ci-webhook
  namespace: fusion
spec:
  chainRef:
    name: ci-demo
  type: Webhook
  webhook:
    path: /trigger/ci-demo
    secretRef:
      name: ci-webhook-token
---
apiVersion: v1
kind: Secret
metadata:
  name: ci-webhook-token
  namespace: fusion
stringData:
  token: "replace-with-a-strong-secret"
```

**Fire from your CI system (GitHub Actions, GitLab CI, etc.):**
```bash
curl -X POST http://<cluster-ip>:9090/trigger/ci-demo \
  -H "Authorization: Bearer replace-with-a-strong-secret" \
  -H "Content-Type: application/json" \
  -d '[{"name":"GIT_SHA","value":"abc1234"}]'
```

---

## Deploy step with codeSource

A Deploy-kind step whose application code comes from a fusion-index artifact instead of the image baked into the WeaveServiceTemplate. The template supplies the runner base image and pod shape; a `code-loader` init container resolves `codeSource.tag` to a concrete version and downloads the artifact into `codeSource.mountPath` (default `/weave-code`) on every pod start, including rolling updates.

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveServiceTemplate
metadata:
  name: order-api
  namespace: fusion
spec:
  image: fusion-runner-python312:local   # runner base image; app code comes from codeSource
  replicas: 2
  ports:
    - name: http
      port: 8080
      targetPort: 8080
  resources:
    requests:
      cpu: 100m
      memory: 256Mi
    limits:
      cpu: 500m
      memory: 512Mi
  readinessProbe:
    httpGet:
      path: /healthz
      port: 8080
    initialDelaySeconds: 10
    periodSeconds: 10
  serviceType: ClusterIP
  unhealthyDuration: "5m"
  revisionHistoryLimit: 3
  codeSource:
    artifactName: app.order-api
    tag: stable
    # mountPath defaults to /weave-code; loaderImage falls back to the
    # LOADER_IMAGE env var, then fusion-code-loader:latest.
  ingress:
    rules:
      - name: order-api            # DNS label only — full host is "order-api.<ingress.hostSuffix>"
        path: /
        pathType: Prefix
        servicePort: http
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveChain
metadata:
  name: order-api-chain
  namespace: fusion
spec:
  steps:
    - name: app
      stepKind: Deploy
      serviceTemplateRef:
        name: order-api
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveTrigger
metadata:
  name: order-api-trigger
  namespace: fusion
spec:
  chainRef:
    name: order-api-chain
  type: OnDemand
```

**What the operator injects automatically:** `WEAVE_ARTIFACT`, `WEAVE_TAG`, `WEAVE_VERSION`, `WEAVE_NAMESPACE`, `WEAVE_MOUNT_PATH`, plus (once fusion-index metadata resolves) `WEAVE_PORT`, `WEAVE_RUNNER_TYPE`, `WEAVE_BUILDER_IMAGE`, `WEAVE_MAINTAINER`, `WEAVE_INGRESS_PATH_PREFIX`, and every key in the artifact's `runner.args` map.

**Keeping it fresh:** the chain controller polls fusion-index every `CODE_SOURCE_POLL_INTERVAL` (default `60s`) and rolling-restarts the Deployment when the tag resolves to a new version. To force one immediately:
```bash
kubectl annotate weavechain order-api-chain fusion-platform.io/reload-deploy-step=app@1.2.3 --overwrite -n fusion
```

**Read-only root filesystem:** if `containerSecurityContext.readOnlyRootFilesystem: true` (set on the template, or inherited from the operator-wide security defaults), both the code-loader and the app container need writable scratch space. `WRITABLE_PATHS` (`codeSource.writablePaths` in Helm) mounts an `emptyDir` at each listed path — default `/tmp`, `/home/nonroot`, `/weave-work` — into both containers, but **only** when `codeSource` is configured on the step. `podSecurityContext` / `containerSecurityContext` set on `WeaveServiceTemplateSpec` override the operator-wide defaults entirely for this template's pods; `containerSecurityContext` applies to the `service` container and the `code-loader` init container alike.

On minikube, set `codeSource.loaderImage` explicitly (e.g. `fusion-weave-operator:<version>`, which embeds `/loader`) — the built-in default `fusion-code-loader:latest` does not exist locally.

---

## Job step with codeSource

Job-kind steps pull code the same way, via the same init container mechanism — but since every WeaveRun creates a brand-new Job pod, the tag is re-resolved fresh on every run. No polling and no rolling restart are needed.

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveJobTemplate
metadata:
  name: report-generator
  namespace: fusion
spec:
  image: fusion-runner-python312:local
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 300m
      memory: 256Mi
  codeSource:
    artifactName: app.report-generator
    tag: stable
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveChain
metadata:
  name: report-chain
  namespace: fusion
spec:
  steps:
    - name: generate
      jobTemplateRef:
        name: report-generator
      producesOutput: true
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveTrigger
metadata:
  name: report-trigger
  namespace: fusion
spec:
  chainRef:
    name: report-chain
  type: Cron
  schedule: "0 0 6 * * *"   # daily at 06:00 (6-field, seconds-first)
```

The same `WEAVE_*` env vars as the Deploy case are injected into the job container, and `codeSource.mountPath` (default `/weave-code`) holds the unpacked artifact by the time the job's command runs.

---

## Multi-tenant service instances (stepOverrides)

One `WeaveChain` can back many independent service instances — each `WeaveRun` supplies its own `artifactName`/`tag`/`ingressName` for a Deploy-kind step via `spec.stepOverrides`, instead of every tenant needing a separate chain. The operator names the Deployment `<runName>-<stepName>` and owns it with the WeaveRun (not the WeaveChain), so runs can come and go independently of each other.

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveServiceTemplate
metadata:
  name: tenant-service
  namespace: fusion
spec:
  image: fusion-runner-python312:local   # runner base image; per-run artifact/tag come from stepOverrides
  replicas: 1
  ports:
    - name: http
      port: 8080          # placeholder — replaced by runner.port from the resolved artifact's metadata.yaml
      targetPort: 8080
  resources:
    requests:
      cpu: 100m
      memory: 256Mi
    limits:
      cpu: 500m
      memory: 512Mi
  serviceType: ClusterIP
  unhealthyDuration: "5m"
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveChain
metadata:
  name: tenant-service-chain
  namespace: fusion
spec:
  steps:
    - name: serve
      stepKind: Deploy
      serviceTemplateRef:
        name: tenant-service
---
# One run per tenant — created directly, no trigger required.
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveRun
metadata:
  name: tenant-acme
  namespace: fusion
spec:
  chainRef:
    name: tenant-service-chain
  stepOverrides:
    - stepName: serve
      artifactName: app.tenant-service
      tag: stable
      ingressName: acme       # full host: acme.<ingress.hostSuffix>
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveRun
metadata:
  name: tenant-globex
  namespace: fusion
spec:
  chainRef:
    name: tenant-service-chain
  stepOverrides:
    - stepName: serve
      artifactName: app.tenant-service
      tag: canary
      ingressName: globex     # full host: globex.<ingress.hostSuffix>
```

**Fire it:**
```bash
kubectl create -f tenant-acme.yaml -n fusion
kubectl create -f tenant-globex.yaml -n fusion
```
Use `kubectl create`, not `kubectl apply` — server-side apply can silently prune a newly-added CRD field for a short window after the CRD is updated, while `create` errors instead of dropping it.

Deleting `tenant-acme` tears down only `tenant-acme-serve` (Deployment, Service, and Ingress if `ingressName` was set) via the `weave.fusion-platform.io/deploy-cleanup` finalizer — `tenant-globex` is unaffected. Each run's `status.activeDeployments` tracks health and rolling-restarts on tag change, same as the chain-owned case.

---

## Shared auth secret — authSecretRef

`WeaveChainSpec.authSecretRef` injects a Secret via `envFrom` into every step pod of the chain (Job and Deploy alike) — useful for a shared OAuth/Keycloak client credential that runner-side helper libraries read to obtain access tokens. It can be overridden per-trigger or per-run.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: keycloak-service-creds
  namespace: fusion
stringData:
  KEYCLOAK_CLIENT_ID: "weave-worker"
  KEYCLOAK_CLIENT_SECRET: "replace-with-a-strong-secret"
  KEYCLOAK_TOKEN_URL: "https://auth.example.com/realms/fusion/protocol/openid-connect/token"
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveChain
metadata:
  name: authenticated-pipeline
  namespace: fusion
spec:
  authSecretRef:
    name: keycloak-service-creds
  steps:
    - name: call-api
      jobTemplateRef:
        name: api-caller
```

Override for a single trigger (e.g. a staging environment with its own client credentials):

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveTrigger
metadata:
  name: authenticated-pipeline-staging
  namespace: fusion
spec:
  chainRef:
    name: authenticated-pipeline
  type: OnDemand
  authSecretRefOverride:
    name: keycloak-service-creds-staging
```

Or per-run only, without touching the trigger:

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveRun
metadata:
  name: authenticated-pipeline-manual
  namespace: fusion
spec:
  chainRef:
    name: authenticated-pipeline
  authSecretRefOverride:
    name: keycloak-service-creds-oneoff
```

Precedence: `WeaveRunSpec.AuthSecretRefOverride` > `WeaveTriggerSpec.AuthSecretRefOverride` > `WeaveChainSpec.AuthSecretRef`.

---

## Failure branch / cleanup step

A cleanup step runs only when an upstream step fails, while the success path is skipped.

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveChain
metadata:
  name: resilient-pipeline
  namespace: fusion
spec:
  failurePolicy: ContinueAll   # keep running even if steps fail, so cleanup can execute
  steps:
    - name: extract
      jobTemplateRef:
        name: extractor

    - name: transform
      jobTemplateRef:
        name: transformer
      dependsOn: [extract]
      runOnSuccess: true
      runOnFailure: false

    - name: load
      jobTemplateRef:
        name: loader
      dependsOn: [transform]
      runOnSuccess: true
      runOnFailure: false

    - name: cleanup
      jobTemplateRef:
        name: cleanup-job
      dependsOn: [extract]
      runOnSuccess: false   # only runs if extract fails
      runOnFailure: true
```

---

## Scheduled run

Fire a chain on a cron schedule. The schedule uses a **6-field cron** with a leading seconds field.

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveTrigger
metadata:
  name: nightly-report
  namespace: fusion
spec:
  chainRef:
    name: etl-pipeline
  type: Cron
  schedule: "0 0 1 * * *"      # every day at 01:00:00
  parameterOverrides:
    - name: REPORT_DATE
      value: "$(date +%Y-%m-%d)"
```

**Cron schedule reference (6 fields: sec min hour dom month dow):**

| Example | Meaning |
|---|---|
| `0 */5 * * * *` | every 5 minutes |
| `0 0 * * * *` | every hour |
| `0 0 6 * * *` | daily at 06:00 |
| `0 0 2 * * 1` | every Monday at 02:00 |
| `0 0 0 1 * *` | first day of every month |

---

## On-demand run

Fire a chain by setting an annotation on the trigger — useful for manual or script-driven execution.

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveTrigger
metadata:
  name: my-trigger
  namespace: fusion
spec:
  chainRef:
    name: etl-pipeline
  type: OnDemand
```

**Fire:**
```bash
kubectl annotate weavetrigger my-trigger fusion-platform.io/fire=true -n fusion

# Watch the run
kubectl get fr -n fusion -w

# See step-level status
kubectl get weaverun -n fusion -o jsonpath='{.items[-1].status.steps}' | python3 -m json.tool
```

---

## Webhook trigger

Fire a chain via an authenticated HTTP POST. Useful for integration with external systems.

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveTrigger
metadata:
  name: api-webhook
  namespace: fusion
spec:
  chainRef:
    name: etl-pipeline
  type: Webhook
  webhook:
    path: /trigger/etl
    secretRef:
      name: etl-webhook-token    # Secret must have key "token"
---
apiVersion: v1
kind: Secret
metadata:
  name: etl-webhook-token
  namespace: fusion
stringData:
  token: "my-secret-token"       # replace with openssl rand -hex 32
```

**Fire:**
```bash
# Get the webhook service address (minikube)
WEBHOOK_IP=$(minikube service fusion-weave-webhook -n fusion --url 2>/dev/null || echo "http://localhost:9090")

curl -X POST ${WEBHOOK_IP}/trigger/etl \
  -H "Authorization: Bearer my-secret-token" \
  -H "Content-Type: application/json" \
  -d '[{"name":"ENV","value":"staging"},{"name":"DRY_RUN","value":"false"}]'
```

The body is an optional JSON array of `{"name":"…","value":"…"}` overrides injected as environment variables into every job in the run.

---

## BatchCron trigger — job list from a ConfigMap

`BatchCron` fires many independently-scheduled jobs off one trigger, driven by a `jobs.yaml` key in a ConfigMap — useful when the set of scheduled jobs is itself data (e.g. per-tenant exports) rather than something you want to template as separate `WeaveTrigger` objects. Each entry carries its own cron schedule and fires a run of the same chain, with per-job metadata injected as env vars.

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveJobTemplate
metadata:
  name: batch-echo
  namespace: fusion
spec:
  image: busybox:latest
  command: ["/bin/sh", "-c"]
  args:
    - |
      echo "job=$JOB_ID name=$JOB_NAME topic=$JOB_TOPIC schedule=$JOB_SCHEDULE"
      echo "metadata=$JOB_METADATA"
  resources:
    requests:
      cpu: 50m
      memory: 32Mi
    limits:
      cpu: 100m
      memory: 64Mi
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveChain
metadata:
  name: batch-jobs
  namespace: fusion
spec:
  steps:
    - name: run
      jobTemplateRef:
        name: batch-echo
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: batch-jobs-list
  namespace: fusion
  labels:
    fusion-platform.io/batch-trigger: batch-jobs   # must equal the WeaveTrigger name below, so ConfigMap edits re-sync it
data:
  jobs.yaml: |
    - job:
        id: "nightly-export"
        name: "nightly-export"
        topic: "reporting"
        maintainer: "ops-team"
        schedule: "0 2 * * *"      # standard 5-field cron — no seconds field (unlike type: Cron)
        metadata:
          kind: export
    - job:
        id: "hourly-sync"
        name: "hourly-sync"
        topic: "sync"
        maintainer: "ops-team"
        schedule: "0 * * * *"
        metadata:
          kind: sync
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveTrigger
metadata:
  name: batch-jobs
  namespace: fusion
spec:
  chainRef:
    name: batch-jobs
  type: BatchCron
  batchCron:
    jobsConfigMapRef:
      name: batch-jobs-list
```

> **5-field vs 6-field cron:** `jobs.yaml` schedules are standard 5-field cron (`min hour dom month dow`), unlike `type: Cron`'s 6-field seconds-first `spec.schedule` (see [Scheduled run](#scheduled-run)). Don't copy one format into the other.

Each fired job receives `JOB_ID`, `JOB_NAME`, `JOB_TOPIC`, `JOB_SCHEDULE`, and `JOB_METADATA` (the `metadata` map, JSON-encoded) as env vars.

**Check it:**
```bash
kubectl get ft batch-jobs -n fusion -o jsonpath='{.status.batchJobCount} jobs loaded, {.status.batchJobErrors} errors'
```

---

## Kafka trigger — S3 event consumption

`Kafka` triggers fire one WeaveRun per matching message — built for consuming MinIO/S3 event notifications forwarded onto a topic (e.g. via Redpanda), but works against any Kafka-compatible broker. `eventFilter`/`bucketFilter` narrow which messages fire a run.

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveJobTemplate
metadata:
  name: s3-event-handler
  namespace: fusion
spec:
  image: busybox:latest
  command: ["/bin/sh", "-c"]
  args:
    - |
      echo "event=$S3_EVENT_NAME bucket=$S3_BUCKET key=$S3_KEY size=$S3_SIZE"
  resources:
    requests:
      cpu: 50m
      memory: 32Mi
    limits:
      cpu: 100m
      memory: 64Mi
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveChain
metadata:
  name: s3-ingest
  namespace: fusion
spec:
  steps:
    - name: handle-event
      jobTemplateRef:
        name: s3-event-handler
---
apiVersion: v1
kind: Secret
metadata:
  name: kafka-sasl-creds
  namespace: fusion
stringData:
  username: "weave-consumer"
  password: "replace-with-a-strong-secret"
  mechanism: "SCRAM-SHA-256"   # optional: PLAIN, SCRAM-SHA-256 or SCRAM-SHA-512 — defaults to PLAIN when omitted
---
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveTrigger
metadata:
  name: s3-ingest-kafka
  namespace: fusion
spec:
  chainRef:
    name: s3-ingest
  type: Kafka
  kafka:
    brokers:
      - "redpanda-0.redpanda.redpanda.svc.cluster.local:9093"
    topic: "s3-events"
    consumerGroup: "fusion-weave-s3-ingest"
    secretRef:
      name: kafka-sasl-creds
    eventFilter: ["put"]        # "put", "delete", "get" — empty = all events
    bucketFilter: ["uploads"]   # empty = all buckets
    maxConcurrentRuns: 5
```

`secretRef` is optional — omit it for a broker with no SASL (e.g. a dev Redpanda with SASL off). The payload must be a MinIO S3 event-notification JSON envelope for `eventFilter`/`bucketFilter` matching and the `S3_*` env vars to populate; other payload shapes are still consumed but won't match filters or populate those vars.

---

## API key setup and usage

Create an API key and use the REST API to manage resources.

```bash
# Generate a key
KEY=$(openssl rand -hex 32)

# Create the Secret
kubectl create secret generic ops-api-key \
  --from-literal=key="$KEY" \
  --namespace=fusion
kubectl label   secret ops-api-key fusion-platform.io/api-key=true  -n fusion
kubectl annotate secret ops-api-key fusion-platform.io/role=editor  -n fusion
# Roles: viewer (GET), editor (GET/POST/PUT/PATCH), admin (all + DELETE)

# Port-forward the API server
kubectl port-forward svc/fusion-weave-api 8082:8082 -n fusion &

# Health check
curl http://localhost:8082/healthz

# List all chains
curl -H "Authorization: Bearer $KEY" http://localhost:8082/api/v1/chains

# Create a chain via API
curl -H "Authorization: Bearer $KEY" \
     -H "Content-Type: application/json" \
     -X POST http://localhost:8082/api/v1/chains \
     -d @config/samples/weavechain_pipeline.yaml

# Get a specific run
curl -H "Authorization: Bearer $KEY" \
     http://localhost:8082/api/v1/runs/my-run-name

# Patch a job template image
curl -H "Authorization: Bearer $KEY" \
     -H "Content-Type: application/merge-patch+json" \
     -X PATCH http://localhost:8082/api/v1/jobtemplates/extractor \
     -d '{"spec":{"image":"python:3.12-slim"}}'

# Delete a trigger (requires admin role)
curl -H "Authorization: Bearer $KEY" \
     -X DELETE http://localhost:8082/api/v1/triggers/old-trigger
```

**API endpoints:**

| Method | Path | Description |
|---|---|---|
| GET | `/api/v1/jobtemplates` | List all WeaveJobTemplates |
| POST | `/api/v1/jobtemplates` | Create a WeaveJobTemplate |
| GET | `/api/v1/jobtemplates/{name}` | Get a WeaveJobTemplate |
| PUT | `/api/v1/jobtemplates/{name}` | Replace a WeaveJobTemplate |
| PATCH | `/api/v1/jobtemplates/{name}` | JSON merge-patch a WeaveJobTemplate |
| DELETE | `/api/v1/jobtemplates/{name}` | Delete a WeaveJobTemplate |
| GET | `/api/v1/servicetemplates` | List WeaveServiceTemplates |
| … | … | Same pattern for `/chains`, `/triggers`, `/runs` |
| GET | `/healthz` | Liveness probe (no auth) |
| GET | `/readyz` | Readiness probe (no auth) |

---

## Monitoring API — observing runs

The monitoring API is read-only and requires at least `viewer` role. Enable it with `MONITORING_ENABLED=true` (or `--set api.monitoring.enabled=true` in Helm).

### Watch a run until completion

```bash
kubectl port-forward svc/fusion-weave-api 8082:8082 -n fusion &

RUN=etl-manual-20260412
while true; do
  RESP=$(curl -s -H "Authorization: Bearer $KEY" \
    http://localhost:8082/monitor/v1/runs/$RUN)
  PHASE=$(echo $RESP | jq -r '.run.status.phase')
  echo "phase: $PHASE"
  [[ "$PHASE" == "Succeeded" || "$PHASE" == "Failed" ]] && break
  sleep 5
done
```

### Fetch step logs

```bash
# Last 100 lines from the "extract" step
curl -s -H "Authorization: Bearer $KEY" \
  http://localhost:8082/monitor/v1/runs/etl-manual-20260412/steps/extract/logs | \
  jq -r '.lines[]'
```

### Aggregate run statistics

```bash
# Last hour (default)
curl -s -H "Authorization: Bearer $KEY" \
  http://localhost:8082/monitor/v1/stats/runs | jq .

# Last 7 days
curl -s -H "Authorization: Bearer $KEY" \
  "http://localhost:8082/monitor/v1/stats/runs?window=7d" | jq .

# Per-chain stats for the past 24 h
curl -s -H "Authorization: Bearer $KEY" \
  "http://localhost:8082/monitor/v1/stats/chains/etl-pipeline?window=24h" | jq .
```

Example response:
```json
{
  "window": "24h",
  "total": 12,
  "succeeded": 10,
  "failed": 1,
  "running": 1,
  "pending": 0,
  "stopped": 0,
  "successRate": 0.909,
  "avgDurationMs": 184200,
  "minDurationMs": 94000,
  "maxDurationMs": 421000
}
```

### Inspect jobs and events for a failed run

```bash
RUN=etl-manual-20260412

# All batch jobs
curl -s -H "Authorization: Bearer $KEY" \
  http://localhost:8082/monitor/v1/runs/$RUN/jobs | jq '.[].metadata.name'

# Kubernetes events (shows warnings, scheduling failures, OOM kills)
curl -s -H "Authorization: Bearer $KEY" \
  http://localhost:8082/monitor/v1/runs/$RUN/events | \
  jq '.[] | select(.type == "Warning") | {reason, message}'
```

### Check deployment health for a chain

```bash
curl -s -H "Authorization: Bearer $KEY" \
  http://localhost:8082/monitor/v1/chains/ci-demo/deployments | \
  jq '.[] | {name: .metadata.name, available: (.status.conditions[] | select(.type == "Available") | .status)}'
```

### Filter all events by reason

```bash
curl -s -H "Authorization: Bearer $KEY" \
  "http://localhost:8082/monitor/v1/events?fieldSelector=reason=BackOff" | jq .
```

### Prometheus metrics

```bash
# Expose the metrics port (separate from the API port)
kubectl port-forward svc/fusion-weave-api 9091:9091 -n fusion &

# Scrape metrics
curl -s http://localhost:9091/metrics | grep -E "^weave_"
```

Key metrics exposed:

| Metric | Type | Description |
|---|---|---|
| `weave_monitor_requests_total` | Counter | Total monitoring API requests by path and status |
| `weave_monitor_request_duration_seconds` | Histogram | Request latency by path |
| `weave_monitor_cache_hits_total` | Counter | Cache hits across all monitoring handlers |
| `weave_monitor_cache_misses_total` | Counter | Cache misses across all monitoring handlers |
| `weave_runs_by_phase` | Gauge | Current run count per phase (Pending/Running/Succeeded/Failed/Stopped) |
