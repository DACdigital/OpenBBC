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
| `openbbcd.replicaCount` | `1` | Migrations auto-apply at startup; do not scale >1 until you add a leader-elected step. openbbcd itself is stateless. |
| `openbbcd.ingress.enabled` | `false` | Otherwise use `kubectl port-forward` to reach `/agents/ui`. |
| `hostAliases` | `[]` | `/etc/hosts` entries on every pod (open-bbcd and the aikdm CronJobs), e.g. an LLM gateway with no DNS record. |
| `aikdmRunner.image.repository` | `ghcr.io/dacdigital/openbbc/aikdm-runner` | Built from `Dockerfile.aikdm-runner` by CI. |
| `aikdmRunner.secrets.existingSecret` | `""` | Recommended for prod. Secret keys are envFrom-loaded verbatim by cron pods — set at least one of `ANTHROPIC_API_KEY`/`OPENAI_API_KEY`/`GEMINI_API_KEY`. |
| `aikdmRunner.extraEnv` | `{}` | Plain env vars on the evals and trainings CronJob containers (not alphas). They override same-named keys from the aikdm Secret. Use it for `AIKDM_MODEL_TARGET` / `_JUDGE` / `_USER_SIMULATOR` / `_TEACHER` / `_GENERATOR` / `_CRITIC` and `AIKDM_EVAL_USER_TURN_MODE` / `AIKDM_EVAL_HISTORY_MODE`. aikdm needs a provider key for all six models at startup, so when you switch provider, set all six to it and put that provider's key in the Secret. Unset → aikdm defaults (`claude-haiku-4-5`, `verbatim`, `simulated`). The BO flags evals/trainings whose target differs from open-bbcd's `OPENBBC_DEFAULT_MODEL`. |
| `aikdmRunner.cronjobs.alphas.schedule` | `*/5 * * * *` | One aikdm LLM call (~30–60s) per finalized version. |
| `aikdmRunner.cronjobs.evals.schedule` | `*/10 * * * *` | Evals are seconds-per-run. |
| `aikdmRunner.cronjobs.trainings.schedule` | `*/15 * * * *` | Trainings are minutes-per-epoch. |
| `seed.enabled` | `false` | Mount a seed ConfigMap at `/etc/openbbc/seed` and set `SEED_PATH`. Needs `seed.files` or `seed.existingConfigMap` (rendering fails otherwise). See "Seeding agents". |
| `seed.files` | `{}` | filename -> YAML string, rendered into `<release>-openbbc-seed`. Changes roll the pod. |
| `seed.existingConfigMap` | `""` | Mount an operator-managed ConfigMap instead (needs a manual `kubectl rollout restart`). |
| `openbbcd.probes.startup` | `failureThreshold: 30`, `periodSeconds: 10` | Startup probe on `/health`; always rendered. |
| `postgres.enabled` | `true` | For prod, set `false` and use `externalDatabase.url`. |
| `postgres.auth.password` | `""` | Empty → random on first install, kept via `helm.sh/resource-policy: keep`. |
| `externalDatabase.url` | `""` | e.g. `postgres://.../openbbcd?sslmode=require`. |
| `externalDatabase.existingSecret` | `""` | Alternative: reference a pre-existing Secret with key `DATABASE_URL`. |

---

## Seeding agents

open-bbcd can import agents, versions and MCP backends from YAML at boot (after
migrations, before it serves). The chart wires this up through the `seed:` values.

1. **Export locally** from a running open-bbcd to an `openbbc-seed-v1` file: use the
   **Export** button on the backoffice version page, or call the API directly:

   ```bash
   curl -si -X POST http://localhost:8080/agent_versions/<version_id>/export \
     -H 'Content-Type: application/json' \
     -d '{"static_ids":{...},"deploy":"if_none"}'
   ```

   Secrets in the export are `${VAR}` placeholders, not values. The response header
   `X-Seed-Env-Vars` lists the `${VAR}` names that must be set in the
   `openbbcd.envFromSecret` Secret (step 3). In CI, run
   `open-bbcd seed validate --no-env <file>` to check the file and list the variables
   it needs.
2. **Put the file in values** under `seed.files` (filename -> YAML string) and enable it:

   ```bash
   helm upgrade --install openbbc deploy/helm/openbbc -n openbbc \
     --set seed.enabled=true \
     --set-file 'seed.files.agents\.yaml'=./agents.yaml
   ```

   The chart renders a ConfigMap `<release>-openbbc-seed`, mounts it read-only at
   `/etc/openbbc/seed` and sets `SEED_PATH=/etc/openbbc/seed`.
3. **Put the `${VAR}` values in the `openbbcd.envFromSecret` Secret** (the existing knob;
   there is no separate seed Secret). Create that Secret before installing.
4. **Install.** A `checksum/seed` pod annotation over `seed.files` rolls the pod whenever
   the seed changes.

To manage the ConfigMap yourself, set `seed.existingConfigMap=<name>` instead of
`seed.files`. The chart can't checksum a ConfigMap it doesn't own, so after editing it run
`kubectl -n openbbc rollout restart deploy/<release>-openbbc-openbbcd`.

A `startupProbe` on `/health` (`openbbcd.probes.startup`, default `failureThreshold: 30`,
`periodSeconds: 10`, i.e. 300s) is always rendered, so liveness can't kill the pod while
migrations and the seed import run. Raise it for large seeds.

Sharp edges:

- **`force` re-pins the deployed version on every restart.** Each time the pod starts, a
  version marked `force` is deployed again unless it is already the deployed one, demoting
  whatever the backoffice deployed since. It only changes which version is deployed; it
  does not revert backoffice edits to agents or versions.
- **Backend takeover by name.** A seeded backend of any kind (`mcp_client` or
  `http_endpoint`) takes over an existing backend with the same name: its config is
  replaced from the file on every boot, and it is marked seed-managed (read-only in the
  backoffice).
- **Boot fails, nothing is imported** when a `${VAR}` has no key in the Secret, when a
  deploy the seed performs hits an endpoint tool with no backend wired, or when a version's
  static ID already exists with different content and the file marks it for deploy
  (`if_none` or `force`). New content needs a new static ID.
- **Default static IDs can collide across clusters.** The export defaults to `<agent>` and
  `<agent>_v<N>`, which another cluster may already use for something else. Choose explicit
  static IDs in the export dialog (or `static_ids`) for anything you ship to clients.
- **URLs are exported as-is.** Backend URLs reach the target environment unchanged; edit
  them in the seed file if staging and production differ.
- Resources removed from the seed are never deleted on boot.

---

## Uninstall

```bash
helm uninstall openbbc -n openbbc
# The postgres PVC (only remaining PVC in the chart) is NOT deleted automatically:
kubectl -n openbbc delete pvc -l app.kubernetes.io/instance=openbbc
# The postgres Secret has the `keep` policy — delete manually if you really want:
kubectl -n openbbc delete secret openbbc-postgres
```

---

## Known limitations / follow-ups

- No horizontal scaling. `open-bbcd` auto-applies migrations on start; running >1 replica needs a leader-elected migration step. The pod itself is stateless (discovery zip lives in Postgres per migration 026), so scaling is a scheduling concern, not a storage one.
- No HPA, no PDB, no NetworkPolicy — this is a smoke-test chart, not production-hardened. Layer those on when the deployment story stabilizes.
- Cron pods currently talk to `open-bbcd` **unauthenticated** over cluster-internal HTTP, matching the current same-trust-boundary story documented in `docs/PRODUCTION.md`. If that changes upstream, add auth to the CronJob env.
