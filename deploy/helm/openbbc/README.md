# openbbc Helm chart

Deploys the OpenBBC platform to Kubernetes:

- **open-bbcd** — the Go service (backoffice UI + REST API + deployed-agent runtime), fronted by a Service and optional Ingress. Auto-applies DB migrations on startup.
- **aikdm cron drainers** — three `CronJob`s that periodically drain `PENDING` alpha generations (finalized versions awaiting bundle generation), `PENDING` evals, and `PENDING` training sessions by calling open-bbcd's REST endpoints. Runs a bundled `aikdm-runner` image (bash + curl + uv + aikdm source + scripts/). The alpha drainer also mounts `DATABASE_URL` because `seed_bundle.py` writes directly to Postgres.
- **Postgres** — optional in-cluster `StatefulSet`. Toggle off and point at a managed DB via `externalDatabase.url` for production.

The chart does **not** build or push container images. See below.

---

## Prerequisites

1. A Kubernetes cluster ≥ 1.25 with a default `StorageClass` that provides `ReadWriteOnce` volumes.
2. `kubectl` context pointing at the target cluster:
   ```bash
   kubectl config current-context
   ```
3. Container registry that both your cluster nodes and your local machine can reach.

---

## Images

`.github/workflows/publish-images.yml` builds and pushes all three images to
GHCR on every PR (same-repo only), every merge to `main`, and every `v*` git
tag. Nothing to do locally — pick a tag and install.

| Trigger | Tag(s) produced |
|---|---|
| PR to `main` | `pr-<num>` |
| Push to `main` | `main`, `sha-<short>` |
| Git tag `vX.Y.Z` | `X.Y.Z`, `X.Y`, `latest` |

The images live at:

- `ghcr.io/dacdigital/openbbc/open-bbcd`
- `ghcr.io/dacdigital/openbbc/aikdm-runner`
- `ghcr.io/dacdigital/openbbc/aikdm` (unused by this chart today; kept in sync)

The chart's default `image.repository` values already point at these paths —
you only need to override `image.tag`.

### Building locally (fork / no-CI escape hatch)

```bash
docker build -t <registry>/open-bbcd:local     -f open-bbcd/Dockerfile        open-bbcd/
docker build -t <registry>/aikdm-runner:local  -f Dockerfile.aikdm-runner     .
docker push       <registry>/open-bbcd:local
docker push       <registry>/aikdm-runner:local
# then set openbbcd.image.repository=<registry>/open-bbcd (etc.) at helm install time.
```

---

## Install (in-cluster Postgres, quick smoke test)

Pick a `TAG` from the table above (e.g. `main`, `pr-50`, `0.1.0`) and:

```bash
TAG=main
helm upgrade --install openbbc deploy/helm/openbbc \
  --namespace openbbc --create-namespace \
  --set openbbcd.image.tag=$TAG \
  --set aikdmRunner.image.tag=$TAG \
  --set aikdmRunner.secrets.ANTHROPIC_API_KEY=sk-ant-... \
  --set openbbcd.ingress.enabled=true \
  --set openbbcd.ingress.hosts[0].host=openbbc.example.com \
  --set openbbcd.ingress.hosts[0].paths[0].path=/ \
  --set openbbcd.ingress.hosts[0].paths[0].pathType=Prefix
```

If your cluster nodes can't pull anonymously from `ghcr.io/dacdigital/…` (i.e.
the package is private), create an `imagePullSecrets` entry for a GHCR PAT and
pass `--set imagePullSecrets[0].name=<secret>`.

## Install (external managed DB)

```bash
TAG=main
helm upgrade --install openbbc deploy/helm/openbbc \
  --namespace openbbc --create-namespace \
  --set postgres.enabled=false \
  --set externalDatabase.url='postgres://user:pass@db.host:5432/openbbcd?sslmode=require' \
  --set openbbcd.image.tag=$TAG \
  --set aikdmRunner.image.tag=$TAG \
  --set aikdmRunner.secrets.existingSecret=aikdm-llm-keys
```

## Dry-run / inspect rendered manifests

```bash
helm template openbbc deploy/helm/openbbc \
  --set openbbcd.image.tag=main \
  --set aikdmRunner.image.tag=main \
  --set postgres.auth.password=hunter2 \
  | less
```

Note: `helm template` (no cluster access) can't `lookup` an existing Secret, so
if you leave `postgres.auth.password=""`, each render produces a fresh random
password. Pin it explicitly for reproducible dry-runs.

---

## Values worth knowing

| Key | Default | Notes |
|---|---|---|
| `openbbcd.image.repository` | `ghcr.io/dacdigital/openbbc/open-bbcd` | Built by `.github/workflows/publish-images.yml`. Override for forks / local builds. |
| `openbbcd.image.tag` | `""` | Empty → `.Chart.appVersion`. Override to `main`, `pr-<num>`, `sha-<short>`, or `X.Y.Z`. |
| `openbbcd.replicaCount` | `1` | PVC is RWO; migrations auto-apply. Do not scale >1. |
| `openbbcd.persistence.size` | `10Gi` | Holds uploaded discovery zips (`FlowMapConfig` sources). |
| `openbbcd.ingress.enabled` | `false` | Otherwise use `kubectl port-forward` to reach `/agents/ui`. |
| `aikdmRunner.image.repository` | `ghcr.io/dacdigital/openbbc/aikdm-runner` | Built from `Dockerfile.aikdm-runner` by CI. |
| `aikdmRunner.secrets.existingSecret` | `""` | Recommended for prod. Secret keys are envFrom-loaded verbatim by cron pods — set at least one of `ANTHROPIC_API_KEY`/`OPENAI_API_KEY`/`GEMINI_API_KEY`. |
| `aikdmRunner.cronjobs.alphas.schedule` | `*/5 * * * *` | One aikdm LLM call (~30–60s) per finalized version. |
| `aikdmRunner.cronjobs.evals.schedule` | `*/10 * * * *` | Evals are seconds-per-run. |
| `aikdmRunner.cronjobs.trainings.schedule` | `*/15 * * * *` | Trainings are minutes-per-epoch. |
| `postgres.enabled` | `true` | For prod, set `false` and use `externalDatabase.url`. |
| `postgres.auth.password` | `""` | Empty → random on first install, kept via `helm.sh/resource-policy: keep`. |
| `externalDatabase.url` | `""` | e.g. `postgres://.../openbbcd?sslmode=require`. |
| `externalDatabase.existingSecret` | `""` | Alternative: reference a pre-existing Secret with key `DATABASE_URL`. |

---

## Uninstall

```bash
helm uninstall openbbc -n openbbc
# PVCs are NOT deleted automatically:
kubectl -n openbbc delete pvc -l app.kubernetes.io/instance=openbbc
# The postgres Secret has the `keep` policy — delete manually if you really want:
kubectl -n openbbc delete secret openbbc-postgres
```

---

## Known limitations / follow-ups

- No horizontal scaling. `open-bbcd` writes to a RWO PVC and auto-applies migrations on start; running >1 replica needs a stateless refactor + external blob storage for `/data/discovery`.
- No HPA, no PDB, no NetworkPolicy — this is a smoke-test chart, not production-hardened. Layer those on when the deployment story stabilizes.
- Cron pods currently talk to `open-bbcd` **unauthenticated** over cluster-internal HTTP, matching the current same-trust-boundary story documented in `docs/PRODUCTION.md`. If that changes upstream, add auth to the CronJob env.
