# redlight survey — secrets, GitOps/Flux, CI + registry (2026-08-02)

**Provenance:** Explore-agent report against the snapshot at `/Users/sofo/Projects/caesar/redlight` (partial copy: no `.git`; `docs/`, `CLAUDE.md`, `tools/`, `assessment/`, `ci-fixture-age.key` absent). **Status: redlight is an IaC pattern donor for caesar, NOT a deployment target** (decided 2026-08-02 — see `docs/brainstorm/2026-08-01-decisions.md` §8). The "minimal file set for caesar" section was written before that decision; kept as reference for a redlight-style k8s deployment.

---

## 1. SECRETS

### Mechanism: SOPS + **age** only. No external-secrets, no sealed-secrets, no Vault-for-secrets.

- **Encryption config:** `kubernetes/secrets/.sops.yaml` — the *only* `.sops.yaml` in the repo. Three ordered `creation_rules` (first match wins), all `age:`, zero `pgp:`/`kms:`:

| rule | `path_regex` | age recipient | meaning |
|---|---|---|---|
| 1 | `^test/.*\.enc\.yaml$` | `age1a6j2n2yvgz9y0q3rce6pq9dnudtd65c4pg7c4mdkdn6vet4uz5lqec6jqd` | CI **fixture** key (private half committed as `ci-fixture-age.key`, gitleaks-allowlisted, **permanent**) |
| 2 | `^prod/.*\.enc\.yaml$` | `age1eax3t5542clp0rvvcqvm5zdn3g9cwfs7sk9q8rv0yzrr6jd5putqu9w903` | **K_prod**, minted 2026-06-25 |
| 3 | `.*\.enc\.yaml$` | `age14rffz4k59fpwgqyxxlxs7yny06v3l505qq9t6m5hfqmywx74l3jswfxhxp` | dev/ci cluster key (catch-all) |

  The header comment states: overlay secrets OUTSIDE `kubernetes/secrets/` (data/infra/obs/app/edge prod overlays) are encrypted with explicit `sops -e --age <K_prod_pub>` — no rule needed. So `.sops.yaml` governs **only** `kubernetes/secrets/**`; every app/overlay `.enc.yaml` elsewhere is encrypted by hand with an explicit `--age` recipient.

- **Vault exists but is NOT a secret store:** `kubernetes/infrastructure/base/vault/release.yaml` is chart `vault` v0.32.0 with `server.dev.enabled: true`, `devRootToken: "root"`, `injector.enabled: false`. Dev-mode, no persistence, unused by any workload.

### Delivery to workloads

Flux's kustomize-controller decrypts at reconcile time. Every app Flux Kustomization carries:

```yaml
  decryption:
    provider: sops
    secretRef:
      name: sops-age
```

The `sops-age` Secret is created **imperatively at bootstrap** (`scripts/hetzner-bootstrap-cluster.sh:727`):

```
kubectl create secret generic sops-age --namespace=flux-system \
  --from-file=age.agekey="${SOPS_AGE_KEY_FILE:?...}"
```

Prod bootstrap first *proves* the key is K_prod (decrypts `kubernetes/secrets/prod/*.enc.yaml` with `XDG_CONFIG_HOME` isolated so ambient identities can't mask it), asserts exactly one `AGE-SECRET-KEY-` line, and asserts `age-keygen -y` == `$SOPS_PROD_AGE_RECIPIENT` — all *before* `flux install` (lines 669–722).

### How a workload consumes a secret

`envFrom.secretRef` is the house pattern (not volume mounts). Example `kubernetes/apps/tap-verifier/platform/deployment.yaml`:

```yaml
          envFrom:
            - configMapRef:
                name: tap-verifier-config
            - secretRef:
                name: tap-verifier-secret
          env:
            - name: DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: redlight-db-app
                  key: uri
```

Plus `imagePullSecrets: [{name: forgejo-registry-pull}]` on every pod.

**Two DB-credential patterns:**
1. **Shared `app` login** — CNPG-generated Secret `redlight-db-app`, key `uri`.
2. **Per-service login** (newer; `approver-api` first) — CNPG managed role added by JSON6902 into the data overlay + a SOPS `basic-auth` Secret as `passwordSecret`, DSN composed via dependent-env expansion (CNPG emits no `uri` for managed roles):
   ```yaml
   - name: DB_PASSWORD
     valueFrom: {secretKeyRef: {name: approver-api-db-credentials, key: password}}
   - name: DATABASE_URL
     value: postgresql://approver_api:$(DB_PASSWORD)@redlight-db-rw.data:5432/app
   ```

### Convention for adding a NEW secret (guard-enforced)

1. File must be named `*.enc.yaml` — `scripts/lint-sops-source-guard.sh` fails any non-`.enc.yaml` file carrying a top-level `sops:` key.
2. **Full-document** encryption is the norm (apiVersion/kind/metadata all `ENC[...]`). The only deliberate deviation: `nats-backup-creds.enc.yaml` uses `--encrypted-regex '^(data|stringData)$'` so identity is statically provable.
3. Live under `kubernetes/apps/<app>/secrets/` with its own `kustomization.yaml`; parent lists `- platform` and `- secrets` (split exists so an `apps-prod` cousin can reference `../../apps/<app>/platform` and swap its own K_prod secret).
4. **Mandatory decryptable CI fixture skeleton** at the mirrored path `kubernetes/secrets/test/fixtures/apps/<same relative path>`, encrypted to the fixture recipient, empty-string values. `scripts/apps-static-validate.sh` hard-fails without it, and enforces "enc-or-fail" (every non-empty scalar under `data:`/`stringData:` must be an `ENC[` token).
5. Owning Flux Kustomization must declare `decryption.provider: sops` — `scripts/lint-decryption-trees.sh` closed a real "Flux wedge" class (its header documents the r17→r20 exploit chain incl. `path.Clean` aliasing).
6. Prod twin: separate `*.enc.yaml` re-keyed to K_prod under `kubernetes/apps-prod/<app>/`, values `REPLACE_AT_PROD_PROVISIONING` until provisioning. `scripts/apps-prod-static-validate.sh` asserts K_prod-only recipients.

**Bootstrap-only secrets** (`kubernetes/secrets/*.enc.yaml`) have no Flux owner; applied by hand: `sops --decrypt ... | kubectl apply -f -`.

### Pinned tooling — `.tool-versions`

`go 1.26.3` · `task 3.51.1` · `opentofu 1.11.5` · `talosctl 1.12.8` · `kubectl 1.35.3` · `flux2 2.8.8` · `cilium-cli 0.19.4` · `cilium-hubble 1.19.3` · `k9s 0.50.18` · `helm 4.1.3` · `kustomize 5.8.1` · `kubeconform 0.7.0` · `kyverno 1.18.1` · `tflint 0.61.0` · `conftest 0.68.2` · `sops 3.12.2` · `age 1.3.1` · `gitleaks 8.21.2` · `pre-commit 4.0.1` · `shellcheck 0.10.0` · `yq 4.44.6` · `promtool 3.5.3` · `loki-logcli 3.5.12` · `rclone 1.68.2` (`go.mod` declares `go 1.26.1` — deliberate-looking skew.)

### Secret scanning

- `.gitleaks.toml`: `[extend] useDefault = true`, **singular** `[allowlist]` (documented: plural `[[allowlists]]` is *silently ignored* in 8.21.2 when extending defaults — verified empirically). Scan is `--no-git` (tree, not history) because a PoC age key is still in history pending a scrub.
- Runs in: `task secret-scan`, pre-commit hook (`gitleaks protect --staged`), and the `secret-scan` CI job (pinned version + SHA256).

---

## 2. GITOPS / FLUX

### Sync topology

- One `GitRepository` `flux-system`, created **imperatively** by `scripts/flux-create-git-source.sh` (`--interval=5m`, creds scraped from local `git credential fill`).
- `flux install --components-extra=image-reflector-controller,image-automation-controller`.
- **No `flux bootstrap`, no parent Kustomization over `kubernetes/flux-system/`.** Every file there is an imperative CR applied by `kubectl apply -f` from the bootstrap script, policed by `scripts/flux-cr-bootstrap-coverage-guard.sh` (every file must appear as an apply line or be on an exclusion list with a reason).
- **`kubernetes/apps/kustomization.yaml` is reconciled by nothing** — it exists so guards can `kustomize build kubernetes/apps`. Adding an app there is still mandatory (guards must see it).

### Standard app Kustomization shape

```yaml
spec:
  interval: 10m
  retryInterval: 30s
  path: ./kubernetes/apps/tap-verifier
  prune: true
  sourceRef: {kind: GitRepository, name: flux-system}
  dependsOn:
    - name: data
  decryption: {provider: sops, secretRef: {name: sops-age}}
  wait: true
  healthChecks:
    - {apiVersion: apps/v1, kind: Deployment, name: tap-verifier, namespace: data}
  timeout: 10m
```

Intervals: GitRepository 5m · app KS 10m/retry 30s/timeout 10m · migrate KS timeout 15m (Job `activeDeadlineSeconds: 900`) · policies KS timeout 5m, no wait, no decryption · ImageRepository 1m · ImageUpdateAutomation 5m · HelmRepository 24h. 15 HelmRepositories in `sources.yaml` (bitnami is `type: oci`).

### The paired `X` / `X-prod` dirs — two separate clusters, not overlays

`rl-ci` (dev) and `rl-prod`; the bootstrap **env-switches which single file it applies**. Prod CRs deliberately reuse the *same* `metadata.name` (single-variant-per-cluster; `flux reconcile ks nui` works on either). Why sibling trees instead of `overlays/prod`: a co-located child overlay referencing `../..` is a kustomize **cycle**, and flat trees can't be directory-referenced under RootOnly load restriction. So prod overlays are **cousins**: `resources: [../../apps/tap-verifier/platform, secret.enc.yaml]`.

Bonus (stated in-repo): `apps-prod/` is outside image-automation's `update.path: ./kubernetes/apps` scan, so prod digests can never be auto-promoted by accident.

Each prod cousin carries exactly three deltas: pinned marker-free digests, K_prod-only secrets, env=prod config. Trap documented on both sides: the cousin doesn't see the parent's *patches*, so prod must duplicate them (JSON6902 `op: add` — a strategic-merge block would *replace* arrays).

`policies-prod` flips only `disallow-latest-tag` Audit→Enforce; everything else stays Audit with a per-policy evidence table. `observability/`, `data/`, `infrastructure/` use the other shape — `base/` + `overlays/{local,ci,prod}` + `components/`.

### Image automation — fully wired

One `ImageRepository`+`ImagePolicy` per image (20 pairs), one repo-scoped `ImageUpdateAutomation`:

```yaml
kind: ImagePolicy
spec:
  imageRepositoryRef: {name: tap-verifier}
  filterTags:
    pattern: ^dev-(?P<ts>[0-9]{14})-[a-f0-9]{12}$
    extract: $ts
  policy: {numerical: {order: asc}}
  digestReflectionPolicy: IfNotPresent
```

```yaml
kind: ImageUpdateAutomation
spec:
  interval: 5m
  git:
    commit:
      author: {name: fluxcdbot, email: fluxcdbot@swap.io}
      signingKey: {secretRef: {name: git-signing-key}}
    push: {branch: main}
  update: {path: ./kubernetes/apps, strategy: Setters}
```

- Tag policy: `dev-<14-digit-timestamp>-<12-hex-sha>`, numerical ascending on extracted timestamp.
- Setter markers in `kustomization.yaml` `images:` blocks, always **paired** tag+digest (`{"$imagepolicy": "flux-system:<name>:tag"}` / `:digest`); `scripts/flux-image-automation-static-validate.sh` enforces block-scoped pairing (stops tag-pinned-but-not-digest-pinned apps).
- Commits GPG-signed via SOPS-encrypted `git-signing-key`; `GitRepository.spec.verify` not enabled (deferred).
- **Webhook-driven, not just polled:** `Receiver/registry-image-push` (fires all ImageRepositories) and `Receiver/git-push`, both `type: github` — notification-controller v1.8.4 has no `forgejo` Receiver type; `generic-hmac` reads only `X-Signature` which Forgejo never sends (live POST → 400); Forgejo sends `X-Hub-Signature-256`, which the `github` receiver validates. Meanwhile commit-status `Provider` is `type: gitea` at `https://git.swap.io/swapdev/redlight` — Receiver and Provider `.spec.type` are different enums.

---

## 3. CI + REGISTRY

### Hosting

Self-hosted **Forgejo v15** at **`https://git.swap.io`**; this repo = `git.swap.io/swapdev/redlight`. Runner image act `ubuntu-24.04`. **No Forgejo runs on the cluster** — every in-cluster `forgejo` reference is a client (`forgejo-registry-pull`, `forgejo-commit-status-auth`).

### Registry

**`git.swap.io`** — Forgejo built-in registry. Namespaces: `git.swap.io/swap/swap-backend/<service>` (19), `swap/swap-frontend`, `swap/swap-admin`. Kyverno `restrict-image-registries` allowlist (`Audit`): `ghcr.io | docker.io | quay.io | registry.k8s.io | git.swap.io`. `verify-image-signatures` matches `git.swap.io/swap/*` (cosign key-based, `Audit`, `verifyDigest: true`); no Rekor for the self-hosted registry — verify pins cosign v2.6.3 with `--insecure-ignore-tlog=true`.

### CI workflows (`.forgejo/workflows/`)

**redlight CI builds ZERO images** — it is IaC validation + a promotion engine:

| file | trigger | purpose |
|---|---|---|
| `static.yaml` | PR + push main + dispatch | validation suite: `validate` matrix shard [lint, helm, scripts, guards] → `task test-ci-shard-<x>` (59 tasks); `static-ok` fan-in (required context "static validation"); `secret-scan` (gitleaks) |
| `promote.yml` | dispatch **only** | one-click prod promotion (mounts write secrets; "may NEVER gain a pull_request trigger") |
| `promote-verify.yml` | every PR | required merge gate for `apps-prod` (no path filter — a required context that never reports blocks merge) |
| `promote-watch.yml` | push main | watcher |
| `dev-deploy-status.yml` | push main + cron */10 | posts deploy status to source repos (per-repo tokens) |

Checkout is hand-rolled (`git init` + `http.extraHeader` auth + `fetch --depth=1`). Concurrency cancels superseded PR runs and `chore(images):` bursts. Matrix job names are interpolated (`static validation (${{ matrix.shard }})`) because Forgejo posts static-named matrix contexts as `<name>`, `<name>-1`, ….

### CI → cluster path

**Dev (automatic, no redlight CI involved):** app repo CI builds & pushes `git.swap.io/...:dev-<ts>-<sha>` → Forgejo `package` webhook → Receiver → ImageRepository scan → ImagePolicy picks newest ts → ImageUpdateAutomation pushes one signed `chore(images)` commit to main → Kustomizations apply → status posted back.

**Prod (manual, gated, digest-only):** `promote.yml` dispatch → skopeo `copy --all --preserve-digests` to immutable `:prod-<ts>-<hex12>` then `:prod` → re-read + assert digest didn't move → yq rewrite of `digest:` lines in `apps-prod/**` (comment-preserving) → refuse if diff touches any non-`digest:` line. `promote-verify.yml` asserts image-set integrity (`scripts/promote-set-guard.sh`, 21 expected images), digest == blessed `:prod`, cosign verify against `security/cosign-prod.pub`. `.forgejo/CODEOWNERS` (RE2) requires code-owner review for `\.forgejo/.*`, `scripts/promote-.*\.sh`, `security/.*`, `Taskfile\.yml`.

### The Go module

`module github.com/bitnet/redlight` — test/tooling only, no application code: `tests/static/` (offline manifest assertions, `-tags static`), `tests/smoke/` + `tests/contract/` (live-cluster, compile-only in CI via `go vet -tags 'smoke contract'`), `tests/internal/harness/` (client-go + port-forward helpers).

---

## Minimal file set for a new app on a redlight-style cluster

*(Historical — written when caesar was to deploy into redlight; kept as the k8s-mode checklist shape.)* App tree under `kubernetes/apps/<app>/` (`kustomization.yaml` → `[platform, secrets]`; deployment with `envFrom`, `imagePullSecrets`, non-root/read-only/drop-ALL/seccomp, httpGet probes — distroless images, no exec probes — requests+limits, `part-of` fleet label); CI fixture skeleton (mandatory); entry in `kubernetes/apps/kustomization.yaml`; Flux CR + `-production` cousin (same `metadata.name`); bootstrap-script apply line (coverage guard); ImageRepository+ImagePolicy pair + Receiver registration; DB via shared-login or managed-role pattern; `apps-prod` cousin with pinned digest + K_prod secret + promote-set-guard registration; tunnel ingress entry if public. Jobs need `kustomize.toolkit.fluxcd.io/force: enabled` (Job specs are immutable).

---

## Surprising / non-obvious

1. **No `flux bootstrap`** — `kubernetes/flux-system/` is imperative CRs + a bash coverage guard; new CRs are invisible to a rebuild unless the bootstrap script is edited.
2. **Dev↔prod is cluster-switched, not overlay-switched** — the *filename* is the environment selector.
3. `kubernetes/apps/kustomization.yaml` is validation scaffolding only.
4. **Vault runs dev-mode with root token `"root"`** and stores nothing.
5. The CI fixture private age key is committed on purpose (permanent allowlist); a different PoC key in history is a tracked finding pending scrub.
6. **`policies-prod` runs almost entirely in Audit in production** — only `disallow-latest-tag` is Enforce; the prod cosign key is still a placeholder.
7. gitleaks scans the tree, not history (`--no-git`), by documented design.
8. Guard scripts are unusually adversarial — several independently re-derive the same facts, with headers narrating reproduced exploits (fake SOPS MAC, shadow Kustomization, `path.Clean` aliasing).
9. gitleaks 8.21.2 silently ignores plural `[[allowlists]]` when extending defaults.
10. Flux `Receiver` needs `type: github` and `Provider` needs `type: gitea` for Forgejo — different enums; a stale-comment guard prevents re-adding the wrong claim.
