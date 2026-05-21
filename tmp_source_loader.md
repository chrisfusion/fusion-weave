# Code-Source Polling, Loader Mechanism & Service Architecture

## 1. How the polling mechanism works

### Goal
When a `WeaveServiceTemplate` step has a `codeSource` field, the operator watches a named artifact tag in **fusion-index** and automatically rolling-restarts the Deployment whenever the tag moves to a new version (i.e. a new stable release is published).

---

### Data stored in `WeaveChain.Status.ActiveDeployments`

Each active deploy step gets an `ActiveDeploymentStatus` entry that carries four fields relevant to polling:

| Field | What it holds |
|---|---|
| `CodeSourceIndexURL` | Base URL of the fusion-index service |
| `CodeSourceArtifact` | Artifact name (e.g. `app.my-service`) |
| `CodeSourceTag` | Tag to watch (e.g. `stable`) |
| `CodeSourceDeployedVersion` | Last semver the operator acted on (e.g. `1.2.0`) |

---

### Polling loop — `syncDeploymentHealth` (`weavechain_controller.go:211`)

The chain reconciler's `syncDeploymentHealth` method iterates over all `ActiveDeployments`. For every entry that has a non-empty `CodeSourceArtifact` it:

1. **Determines the poll interval** — uses `r.CodeSourcePollInterval` (set from `CODE_SOURCE_POLL_INTERVAL` env var, default 60 s).
2. **Sets the requeue deadline** — returns `minRequeue = pollInterval` so the reconciler automatically re-fires after that duration.
3. **Calls `indexclient.ResolveTag`** — two HTTP calls to fusion-index:
   - `GET /api/v1/artifacts?name=<artifact>` → resolves the artifact name to its numeric ID.
   - `GET /api/v1/artifacts/<id>/versions` → walks the version list, finds the version whose tags include the watched tag, returns the semver string.
4. **Compares versions** — if the resolved version differs from `CodeSourceDeployedVersion`, a version bump has occurred.
5. **Calls `triggerCodeReload`** — patches the Deployment's pod template with:
   - `kubectl.kubernetes.io/restartedAt` → forces a rolling restart.
   - `fusion-platform.io/code-source-version` → records the new version for observability.
6. **Updates `CodeSourceDeployedVersion`** in chain status to the new version, so the next poll cycle is a no-op unless the tag moves again.

This runs **inside the health loop** (not in a separate goroutine) using an `if/else` structure specifically so it executes for every entry regardless of that entry's deployment health state.

---

### External / manual trigger — `handleCodeReload` (`weavechain_controller.go:364`)

CI/CD, webhooks, or the REST API can bypass the poll interval by setting the annotation:

```
fusion-platform.io/reload-deploy-step: <stepName>@<version>
```

`handleCodeReload` runs at the top of every reconcile, before `syncDeploymentHealth`. It:
1. Reads and immediately **consumes** (deletes) the annotation — one-shot semantics.
2. Finds the matching `ActiveDeployments` entry by step name.
3. Calls the same `triggerCodeReload` function.
4. Updates `CodeSourceDeployedVersion` so the polling loop doesn't fire a second restart on the next cycle.

---

### Summary of flow

```
Reconcile loop fires (every ~60s via requeue)
  └─ handleCodeReload()        ← annotation path (CI/CD / REST API)
  └─ syncDeploymentHealth()
       └─ for each ActiveDeployment with CodeSourceArtifact:
            └─ indexclient.ResolveTag(indexURL, artifact, tag)
                 ├─ GET /api/v1/artifacts?name=…  → artifact ID
                 └─ GET /api/v1/artifacts/{id}/versions → semver
            └─ if resolved != deployedVersion:
                 └─ triggerCodeReload()  → patch Deployment (rolling restart)
                 └─ update CodeSourceDeployedVersion in chain status
       └─ return minRequeue = pollInterval  → schedules next poll
```

---

## 2. What happens with the code after reload

### 1. Kubernetes rolling restart

`triggerCodeReload` patches two annotations onto the Deployment's **pod template**:

```
kubectl.kubernetes.io/restartedAt  = <current UTC time>
fusion-platform.io/code-source-version = <new semver>
```

Because the pod template changed, Kubernetes starts a **rolling update** — new pods come up while old ones stay live until the new ones pass the readiness probe.

---

### 2. Each new pod runs the `code-loader` init container first

The Deployment injects a `code-loader` init container (the `/loader` binary built into the operator image) that runs **before** the main service container starts.

The init container receives four env vars:

| Env var | Value |
|---|---|
| `INDEX_URL` | fusion-index base URL |
| `ARTIFACT_NAME` | artifact name (e.g. `app.my-service`) |
| `ARTIFACT_TAG` | mutable tag (e.g. `stable`) |
| `MOUNT_PATH` | where to unpack (default `/weave-code`) |

---

### 3. The loader fetches and unpacks the new code (`cmd/loader/main.go`)

**Current behaviour (gap):** `firstFile` picks only the first file for the version and downloads that one. For app builds, fusion-index holds three files per version:

1. `{name}-{version}.tar.gz` — the venvpack
2. `main.py` — the entry point
3. `metadata.yaml` — the app manifest

The loader currently fetches only the venvpack. `main.py` and `metadata.yaml` are not downloaded. **This needs fixing** — the loader must download all files for the version, unpacking archives and writing non-archive files (`.py`, `.yaml`) directly to `mountPath`.

After the fix, the loader steps are:

1. `GET /api/v1/artifacts?name=<artifact>` → artifact ID (from index, authoritative)
2. `GET /api/v1/artifacts/<id>/versions` → resolve tag to semver (from index, authoritative)
3. `GET /api/v1/artifacts/<id>/versions/<ver>/files` → list all files
4. For each file: `GET .../files/<id>/download` → download
   - `.tar.gz` / `.tgz` → unpack into `mountPath`
   - `.zip` → unpack into `mountPath`
   - other (`.py`, `.yaml`) → write directly to `mountPath`
5. Write `<mountPath>/.version` from the index-resolved semver (not from `metadata.yaml` content)

---

### 4. Main container starts with the new code already in place

The `weave-code` volume is an `emptyDir` shared between the init container and the `service` container. By the time the init container exits successfully, all three files are in place. The main container sees the new version from the first moment it runs.

---

### Summary sequence

```
triggerCodeReload()
  └─ patch Deployment pod template (restartedAt, code-source-version)
       └─ Kubernetes rolling update begins
            └─ new pod starts
                 └─ init container: code-loader
                      ├─ resolve tag → semver (index, authoritative)
                      ├─ download all 3 files (venvpack, main.py, metadata.yaml)
                      ├─ unpack venvpack to /weave-code
                      ├─ write main.py, metadata.yaml to /weave-code
                      └─ write /weave-code/.version (from index resolution)
                 └─ service container starts
                      └─ reads code from /weave-code (already at new version)
            └─ old pod terminated after readiness probe passes
```

The tag is re-resolved **at pod start time by the init container**, not at reload-trigger time.

---

## 3. Source of truth: index vs metadata.yaml

| Field | Source of truth | Reason |
|---|---|---|
| artifact name | fusion-index (artifact record) | Index is what was actually published |
| version / semver | fusion-index (version record) | Index is what was actually published |
| `runner.type` | `metadata.yaml` (fetched from index files) | Not exposed in the index version record |
| `runner.port` | `metadata.yaml` | Not exposed in the index version record |
| `runner.args` | `metadata.yaml` | Runtime args for the runner (e.g. `ENTRYPOINT`) |
| `resources` | `metadata.yaml` | CPU/memory requests and limits |
| `ingress.pathPrefix` | `metadata.yaml` | Path prefix for ingress routing |
| `ingressHost` | `WeaveRun.stepOverrides` | Deployment topology — not part of the artifact |

`metadata.yaml` is the **input** to fusion-forge (it drives the build). The index is the **output** (authoritative record of what was published). The operator never reads `name` or `version` from `metadata.yaml` — only fields the index record does not expose.

---

## 4. metadata.yaml format (improved)

Current format has issues: flat resource fields, ambiguous CPU units, verbose runner args, ungrouped ingress config.

**Recommended format:**

```yaml
name: test-template          # forge input only — operator reads name from index
version: "0.1.0"             # forge input only — operator reads version from index
maintainer: some.maintainer@example.local
builderImage: python3.12
basedependencies: ""

runner:
  type: streamlit
  port: 8501
  args:
    ENTRYPOINT: "main.py"    # becomes env var on the container

ingress:
  pathPrefix: testproject

resources:
  requests:
    cpu: "200m"
    memory: "500Mi"
  limits:
    cpu: "500m"
    memory: "1Gi"
```

Key improvements over current format:
- `resources` block matches Kubernetes spec exactly — no translation in deploybuilder
- `runner.args` as a map — directly usable as env vars
- `runner.port` — removes hardcoded port assumption from operator config
- `ingress.pathPrefix` — grouped, clearly named
- CPU values include units (`m`) — unambiguous

---

## 5. Architecture for 200 services + 70 ETL chains

### Design decision: WeaveRun with step overrides

Rather than 200 WeaveChains (near-identical) or a new CRD, the right model is **step overrides on WeaveRun**. WeaveRun is already the execution primitive and already stays `Running` indefinitely for deploy-step chains — it IS the service instance.

**Changes required to the existing model:**
- Add `StepOverrides []WeaveRunStepOverride` to `WeaveRunSpec`
- Deployment naming for override runs: `<runName>-<stepName>` (run-owned, not chain-owned)
- Health monitoring and code-source polling for run-owned deployments move to the run controller
- Teardown: run controller already handles deploy cleanup via the `deploy-cleanup` finalizer

Non-override runs (ETL chains) are **unchanged** — `<chainName>-<stepName>`, chain-owned, current behaviour exactly as today.

### WeaveRun per service (example)

```yaml
apiVersion: weave.fusion-platform.io/v1alpha1
kind: WeaveRun
metadata:
  name: my-service
spec:
  chainRef: streamlit-app
  stepOverrides:
  - stepName: deploy
    artifactName: app.my-service
    tag: stable
    ingressHost: my-service.example.com
```

### Data flow at deploy/poll time

```
WeaveRun.stepOverrides (artifactName, tag, ingressHost)
  └─ indexclient: resolve tag → version        (index, authoritative)
  └─ indexclient: fetch metadata.yaml
       → runner.type     → select runner image from operator config map
       → runner.port     → container port
       → runner.args     → env vars on container
       → ingress.pathPrefix → ingress path
       → resources       → K8s resource requests/limits
  └─ deploybuilder: build Deployment
       name:        <runName>-deploy
       ingressHost: from WeaveRun.stepOverrides
       image:       from runner.type → operator config map
```

### Object counts

| Kind | Count | Notes |
|---|---|---|
| WeaveChain | 71–81 | 1 shared `streamlit-app` + 70–80 distinct ETL chains |
| WeaveServiceTemplate | 1 | shared streamlit runner defaults |
| WeaveJobTemplate | 1 + N | 1 smoketest + N ETL job templates (shared where reused) |
| WeaveRun | 200 + ETL executions | 200 permanent service instances + ETL runs as needed |

### Operator config map (runner type → image)

One small mapping table in Helm values — the only thing that cannot come from the artifact and does not belong in the WeaveRun override:

```yaml
runnerImages:
  streamlit: streamlit-runner:latest
  fastapi:   fastapi-runner:latest
```
