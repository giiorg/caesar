# redlight survey — data layer (CNPG/NATS/Redis) + edge (2026-08-02)

**Provenance:** Explore-agent report against the snapshot at `/Users/sofo/Projects/caesar/redlight` (no `.git`; extracted from zip). **Status: redlight is an IaC pattern donor for caesar, NOT a deployment target** (decided 2026-08-02 — see `docs/brainstorm/2026-08-01-decisions.md` §8). The "what caesar would do" sections describe a redlight-style k8s deployment; caesar V1 deploys compose-first. The **edge findings transfer directly** (cloudflared container in compose uses the same config shape).

---

## 1. DATA LAYER

### CNPG operator

- `kubernetes/data/cnpg-operator/release.yaml` — HelmRelease `cnpg-operator`, ns `cnpg-system`, chart `cloudnative-pg` **0.28.2** → **CNPG 1.29.1**. CRDs `CreateReplace`.
- `plugin-barman-cloud.yaml` — chart **0.6.0** (appVersion v0.12.0), `dependsOn: cnpg-operator`. Backups use the **CNPG-I Barman Cloud plugin**, not the deprecated in-core `spec.backup.barmanObjectStore`.

### Cluster resources — exactly two

**`redlight-db`** (ns `data`) — the shared money-path cluster: `instances: 3`, `ghcr.io/cloudnative-pg/postgresql:18.3` (**PG 18.3**), `primaryUpdateStrategy: unsupervised`, `enablePDB: true`, `storage 50Gi` + separate `walStorage 30Gi` (bumped after a 2026-07-06 incident), no `storageClassName` (default `hcloud-volumes`), requests 1CPU/2Gi limits 2CPU/4Gi. Synchronous replication RPO≈0:

```yaml
synchronous: {method: any, number: 1, dataDurability: required, failoverQuorum: true}
```

TLS-only client auth (load-bearing for any client):

```yaml
pg_hba:
  - hostnossl all all all reject
  - hostssl all all all scram-sha-256
```

Parameters: `max_connections 200`, `shared_buffers 1GB`, `effective_cache_size 3GB`, **`archive_timeout 30s`** (bounds PITR RPO <1 min; CNPG default 5 min), `max_slot_wal_keep_size 10GB`, `pgaudit.log "ddl,role"`. Hard pod anti-affinity on hostname. No `bootstrap` block → CNPG default `initdb`: database `app`, role `app`, generated Secret `redlight-db-app` (keys `username`, `password`, `uri`, …). `monitoring.enablePodMonitor: false` (repo owns explicit PodMonitor). Dev-sizing component removes walStorage, shrinks to 5Gi; local overlay drops to 1 instance, strips `synchronous`.

**`glitchtip-db`** (ns `glitchtip`) — the "app gets its own Cluster" precedent: `instances: 1`, same image, 5Gi (prod cousin patches 20Gi). **No `spec.plugins`, no ObjectStore → NO backups, NO PITR.** Only `redlight-db` is backed up.

### Backup / PITR — Hetzner Object Storage via Barman plugin

`kubernetes/data/components/backup/objectstore.yaml`:

```yaml
kind: ObjectStore
metadata: {name: redlight-pg-store, namespace: data}
spec:
  retentionPolicy: "30d"
  configuration:
    destinationPath: s3://redlight-pg-backups          # ci overlay → -ci
    endpointURL: https://fsn1.your-objectstorage.com   # Hetzner Object Storage
    s3Credentials: {accessKeyId: {name: pg-backup-s3-creds, key: ACCESS_KEY_ID}, ...}
    wal:  {compression: zstd,   maxParallel: 8}
    data: {compression: snappy, jobs: 2}
```

**Hetzner-specific sidecar env — all three required, omission = silent SigV4 failure:**

```yaml
AWS_REQUEST_CHECKSUM_CALCULATION: when_required
AWS_RESPONSE_CHECKSUM_VALIDATION: when_required
AWS_REGION: fsn1     # else SigV4 signs for us-east-1 → SignatureDoesNotMatch
```

Sidecar limits 2CPU/1Gi (CFS throttling of the 30s zstd archiver; OOMKills at 256Mi during bootstrap restore). Plugin attach: `plugins: [{name: barman-cloud.cloudnative-pg.io, isWALArchiver: true, parameters: {barmanObjectName: redlight-pg-store, serverName: redlight-db}}]`. `ScheduledBackup`: 6-field cron `0 0 3 * * *`, `immediate: true`, `method: plugin`. Backup component applied by prod+ci overlays only; local has none. **PITR restore is documented example YAML + runbook, never automated** (`recovery-cluster.yaml.example` = throwaway drill; `recovery-promotion.yaml.example` = restored cluster archiving to a distinct bucket). Other Hetzner buckets: `redlight-nats-backups-{prod,ci}` (7d via rclone), `redlight-kms-wallet-backups-*`, `redlight-kms-wallet-offsite-*`.

### Monitoring

PodMonitor `redlight-db` (ns `monitoring`, port 9187) + ~20 alerts in `prometheusrule.yaml` — `CNPGInstanceDown`, `CNPGNoPrimary`, `CNPGSyncReplicasAtRisk`, `CNPGWALArchiveBacklog`, `CNPGBackupFailing`, `CNPGBackupTooOld` (>26h), `CNPGNoRecoverabilityPoint`. Backup alerts use the **plugin** metric family `barman_cloud_cloudnative_pg_io_*` (in-core `cnpg_collector_*backup*` gauges stop updating under the plugin). **Every alert is hardcoded to `pod=~"redlight-db-[0-9]+"`** → a new Cluster gets zero alerting for free.

### Pooler

Pooler `redlight-db-pooler-rw` (PgBouncer, 2 instances, transaction mode) is **deploy-but-unused** — every app DSNs straight at `redlight-db-rw.data:5432`.

### How an app gets a database

No per-app database. One shared cluster, one shared database `app`, identity separation at the **role** level: (1) legacy shared login `app` via `redlight-db-app` `uri` (~16 apps); (2) newer per-service CNPG **managed roles** (`approver_api` with `connectionLimit: 20`, `inRoles`, SOPS `passwordSecret`; `pgweb_ro` with `inRoles: [pg_read_all_data]`; revocable `obs-agent-db-ro-{1,2,3}`) — DSN composed via `$(DB_PASSWORD)` env expansion; managed roles carry no GUC fields (statement_timeout = manual `ALTER ROLE`). (3) **Network reachability is a separate explicit grant**: CNP `redlight-db-instances-ingress` — the moment any CNP selects an endpoint it becomes ingress default-deny; allowed sources on :5432 are the fleet label `part-of: swap-backend`, the pooler, same-cluster pods, plus a hand-added pgweb rule.

### NATS (donor context only — caesar core is NATS-free)

Chart 2.12.6, ns `data`, 3 replicas. JetStream ON (file-backed 10Gi PVC, `maxSize 8Gi`, memory store off). **Auth: single account, `no_auth_user: app`, and `app` has wildcard publish/subscribe — every backend app connects with no credentials and full access.** Restricted users exist only for `nui_ro`, `obs-agent-nats-ro-*`, `approver_api`. Hot-reload works inside `authorization{}`; changing `no_auth_user` needs attended rolling restart.

### Redis / Valkey

Bitnami `redis` 25.3.9, Sentinel replication ×3, digest-pinned images (Bitnami removed versioned public tags). GlitchTip runs its own standalone `valkey` instead.

### pgweb

`sosedoff/pgweb:0.16.2` digest-pinned, `--readonly --lock-session --no-ssh`, user `pgweb_ro` against the **replica** service (`redlight-db-ro`), CA Secret generated into its ns by a Kyverno generate policy. Coupled-secret hazard documented: the password lives in two SOPS files (pgweb DSN + CNPG passwordSecret) that must stay equal.

### What a caesar-style app would do on a redlight-style cluster (k8s-mode reference)

1. Own CNPG Cluster modeled on `glitchtip-db` + everything glitchtip lacks; copy `pg_hba` TLS-only posture.
2. Own ObjectStore (own bucket, all three Hetzner env vars) + plugin attach + ScheduledBackup + `archive_timeout: 30s`.
3. Simplest credentials: CNPG default bootstrap Secret `<cluster>-app` with ready-made `uri`.
4. **Network policy mandatory:** instance-ingress (:5432 app, :9187 monitoring, :8000 cnpg-system, self replication) + additive egress (kube-dns L7, `toFQDNs` object storage, `toEntities: [kube-apiserver]`) + the bootstrap-jobs egress equivalent (`cnpg.io/jobRole Exists`) — **without it greenfield `initdb` hangs on `wait-for-get-cluster`**.
5. Own PodMonitor + PrometheusRule (existing exprs won't match).
6. Skip the pooler.

---

## 2. EDGE — no ingress controller, no LB, no ACME; 100% Cloudflare Tunnel

Verified zero: `kind: Ingress`, IngressClass, ingress-nginx/traefik/envoy, `type: LoadBalancer`, Gateway API (`gatewayAPI.enabled: false`, guard-enforced). Cilium 1.19.4, kubeProxyReplacement, WireGuard node encryption, KubePrism `127.0.0.1:7445`. `hcloud-ccm` 1.31.1 installed for node lifecycle only — never creates an LB. cert-manager v1.20.1 exists but issues only **internal** certs (`selfsigned-issuer` + an internal OTLP CA). **No Let's Encrypt, no external-dns** — DNS records are OpenTofu `cloudflare_dns_record` CNAMEs → `<tunnel_id>.cfargotunnel.com`, proxied.

### Tunnel stack

Chart `cloudflare-tunnel` **0.3.2**; four env tunnels ×2 replicas + a CI-smoke tunnel; nginx-unprivileged static canaries for edge liveness. Every ingress rule is exactly:

```yaml
- hostname: swap-rd-prod-pgweb.swap.io
  service: http://pgweb.pgweb.svc.cluster.local:8081
  originRequest:
    connectTimeout: 30s
```

Hostname families: ops plane `swap-rd-<env>-<service>.swap.io` (legacy, partly disabled) and product/domain-split `prod-<service>.drsw.io` / `dev-<service>.drsw.io`, apex `swap.io` + `app-api.swap.io`.

### TLS + auth

Public TLS is Cloudflare's; every origin is plain `http://` in-cluster. Admin UIs gated by **Cloudflare Access** (terraformed): `type = self_hosted`, `session_duration = 24h`, `auto_redirect_to_identity`, policy = **email-domain** allow (tfvars note: "tighten to per-email allowlist before go-live"). Deliberately un-gated (each justified in comments): ntfy (own ACLs; phone app can't send Access headers), tatum-webhook (HMAC), faro (anonymous browser posts), **approver (app-level WebAuthn — the "app does its own auth" precedent)**, flux-webhook (HMAC), public api/frontend.

### Timeouts / buffering — the SSE-critical findings

- **The only edge timeout configured anywhere is `connectTimeout: 30s` (TCP dial).** No read/response timeout, no buffering config, nothing that cuts a long-lived stream.
- cloudflared speaks HTTP/1.1 to origins (chunked encoding on); WebSockets supported by default with zero config; no L7 proxy exists anywhere in the path (Cilium httpV2 metrics comment confirms no Envoy in any data path).
- **Existing long-lived-stream precedent: ntfy** — publicly tunneled, subscribers hold SSE/JSON streams, needs **no tunnel-side tuning** (`behind-proxy: true`, cache 12h).
- Real caveats outside the repo: Cloudflare edge ~100s time-to-first-byte limit (error 524) applies to the *initial* response → SSE handlers must flush headers immediately; belt-and-braces headers `Content-Type: text/event-stream`, `Cache-Control: no-cache, no-transform`, `X-Accel-Buffering: no`. cloudflared rolling restarts (2 replicas, PDB 1) break streams → client `EventSource` auto-reconnect + `Last-Event-ID` resume required.
- Repeated lesson in the cloudflared egress NetworkPolicies: **Cilium enforces egress against the backing pod port after socket-LB translation** — if Service port ≠ containerPort you must allow BOTH (guarded by `scripts/lint-cloudflared-egress.sh`). Cleanest fix used by newer apps: make Service port == containerPort.

### Adding a public host (redlight k8s mechanism, 4 steps)

1. Tunnel ingress entry in `edge/*/release.yaml`; 2. cloudflared egress NP rule (both ports if split); 3. Terraform CNAME + optional Access app/policy + `manage_<x>_edge` boolean; 4. Namespace with PSA labels + Kyverno gates (registries `ghcr.io|docker.io|quay.io|registry.k8s.io|git.swap.io`, cosign, non-root, probes, limits, labels — several policies match an explicit namespace list; a new namespace is silently exempt until added).

**Transfer to caesar's compose mode:** the cloudflared *config shape* (hostname → local service, `connectTimeout`) is identical as a compose service; Access optional in front of caesar's own auth; SSE/WS obligations live entirely in caesar's Go handlers.

### Surprises worth flagging

- `hcloud-ccm` installed but never load-balances; every "which ingress annotation" instinct is wrong — the answer is always tunnel entry + NP rule + CNAME.
- The single Access gate is `email_domain` — anyone on the domain reaches pgweb (which holds `pg_read_all_data`), acknowledged in tfvars.
- Edge rate limiting / WAF is deliberately a Cloudflare **dashboard** step (shared zone; a terraformed `http_ratelimit` ruleset would claim the zone's single entry point).
- **Path drift:** dirs are `edge/drswap-{dev,prod}-cloudflare-tunnel` + terraform `swap/drswap-{dev,prod}`, but ≥6 scripts reference the old `swap-{dev,prod}` paths (`edge-static-validate.sh`, `lint-cloudflared-egress.sh`, `pg-static-validate.sh`, `faro-collector-static-validate.sh`, `alerting-static-validate.sh`, `cloudflare-render-tunnel-secret.sh`) — those validators cannot be passing against this tree as-is.
- This snapshot is not a git repo (sits next to `redlight.zip`) — blame/dating unavailable.
