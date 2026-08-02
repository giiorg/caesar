# redlight survey — nodes/topology, Cilium policy, Tetragon (2026-08-02)

**Provenance:** Explore-agent report against the snapshot at `/Users/sofo/Projects/caesar/redlight` (no `.git`, no `docs/` — ~40 dangling doc references; design rationale survives in unusually rich YAML/script header comments). **Status: redlight is an IaC pattern donor for caesar, NOT a deployment target** (decided 2026-08-02 — see `docs/brainstorm/2026-08-01-decisions.md` §8). The Cilium FQDN patterns feed caesar's k8s runner mode; the **Tetragon material transfers to compose mode too** (standalone-capable). The topology findings are what motivated rejecting redlight's clusters as an agent home.

---

## 1. Nodes / topology

### Module `terraform/modules/hetzner-cloud/main.tf`

Two roles only, `count`-driven, no pool abstraction: `rl-cp-${i}` / `rl-worker-${i}` (+ `<cluster>-wg-bastion`). Only heterogeneity mechanism: per-index `worker_types[i]` server-type override — no per-index labels or taints. Hetzner labels (`cluster`, `role`) are cloud labels, never propagated into k8s.

### The two live clusters

- **rl-ci** (fsn1): 3× cpx32 CP + 3× cpx32 workers, **CPs schedulable** (`allowSchedulingOnControlPlanes: true`, `STRIP_CP_TAINT` forced for non-prod) → 6 schedulable nodes. Talos image 392524994 (v1.12.8 amd64). WG bastion, no IPv6.
- **rl-prod** (fsn1): 3× ccx23 CP (dedicated 4c/16G; ADR-0001 — arm cax21 sold out, ci hit CP memory squeeze on 2c/4G) + 3× ccx33 workers (dedicated 8c/32G; **~46% CPU steady / ~69% under N-1**), CP placement group on, **CPs NOT schedulable** and the bootstrap *refuses* `STRIP_CP_TAINT=true` on the production root ("prod data must stay worker-only") → 3 schedulable nodes shared by everything. Prod Talos image is a separate prod-project snapshot. LUKS2 nodeID-derived disk encryption of state+ephemeral.

Bastion cx23, sole public ingress udp/51820; **no control-plane LB at all** (removed; a rego policy denies any `hcloud_load_balancer_service` on :6443).

### Versions

Talos v1.12.8; kernel 6.18-talos; k8s not pinned anywhere (whatever talosctl defaults) — PSA pins `enforce-version/audit-version: v1.35`, kubectl 1.35.3 → 1.35.x. CNI `none` + kube-proxy disabled (Cilium replaces), KubePrism :7445.

### Node pools / labels / taints / autoscaling — the honest answer

Zero hits repo-wide for `nodeLabels`, `nodeTaints`, `register-with-taints`, `nodeSelector`, autoscaler/HPA/VPA, `ResourceQuota`, `LimitRange`, `PriorityClass` (only consumed: `system-node-critical`), `RuntimeClass`/gvisor/kata. Tolerations only as DaemonSet blanket `operator: Exists` (alloy, tetragon, local-path). Anti-affinity is pod-level only (CNPG, NATS, KMS backup jobs).

**Two hard blockers for a dedicated pool on redlight itself:** (1) the prod Talos generator *refuses* `TALOS_PATCH_WORKER` (all workers share one machine config; only `TALOS_PATCH_ALL` exists); (2) `naming_conventions.rego` + module tftest pin server names to `^rl-(cp|worker)-\d+$`.

**Conclusion:** everything shares the same nodes; in prod that is 3 workers also hosting Postgres/NATS/Redis/KMS fund-signers. No natural home for an untrusted batch class — no pool, no label, no taint, no quota, no sandbox runtime. *(This finding drove caesar's pivot to its own dedicated box.)*

---

## 2. Cilium and network policy

### Install (`kubernetes/infrastructure/base/cilium/release.yaml`)

Chart **1.19.4**, ns kube-system:

```yaml
ipam: {mode: kubernetes}
kubeProxyReplacement: true
k8sServiceHost: 127.0.0.1
k8sServicePort: 7445
encryption: {enabled: true, type: wireguard}
hubble: {enabled: true, relay: {enabled: true}, ui: {enabled: true}}
gatewayAPI: {enabled: false}
```

Hubble flow-log export to `/var/run/cilium/hubble/events.log` with `allowList: [DROPPED verdicts, dns_query, destination reserved:world]`, tailed by Alloy → Loki 30d. **DNS proxy has no explicit toggle — it engages implicitly wherever a CNP carries an L7 `rules.dns` block.** No hostFirewall, no cluster-wide policyAuditMode. A bootstrap-time copy of hubble values must stay byte-identical (lint script).

### Policy conventions

**Zero CiliumClusterwideNetworkPolicy; 43 namespaced CNPs.** Default-deny comes in three flavors:

1. **Kyverno-generated, INGRESS-ONLY** — opt-in by namespace label `security.redlight.bitnet/default-deny: enabled` → generates `NetworkPolicy/default-deny-ingress` (`generateExisting`, `synchronize`). **Never denies egress.**
2. **Cilium "network floor"** — namespaced CNP `endpointSelector: {}` with an `enableDefaultDeny` **object** (a scalar is silently ignored — guard-enforced), introduced additively (`false/false`) and flipped per-direction by overlay patch. Carries an inert `- fromEntities: [none]` ingress anchor because Cilium engages default-deny only for a direction that has a rule section. `data` floor: true/true in ci+prod. `monitoring` floor: egress true, **ingress false permanently** (accepted risk LOW-1).
3. **Hand-written vanilla NP `default-deny`** (`podSelector: {}`, both directions) in `web` and `admin`.

**The universal idiom in every CNP** — kube-dns L7 rule, mandatory both to survive default-deny and for `toFQDNs` IP learning:

```yaml
- toEndpoints:
    - matchLabels:
        k8s:io.kubernetes.pod.namespace: kube-system
        k8s-app: kube-dns
  toPorts:
    - ports: [{port: "53", protocol: ANY}]
      rules:
        dns: [{matchPattern: "*"}]
```

(Spelling inconsistency exists: floors write `k8s:k8s-app`, app CNPs bare `k8s-app`; both resolve.)

**The best template for "short-lived Job, world-only FQDN egress" — `kubernetes/data/components/kms-wallet-backup/offsite-egress.yaml`, quoted in full** (SA + vanilla-NP DNS + CNP):

```yaml
# SEC-01 offsite egress. The offsite jobs carry app.kubernetes.io/name=kms-offsite so they do NOT
# inherit kms-wallet-backup-egress-s3 (Hetzner-only, default-deny) — this pair is their complete
# egress surface: DNS floor + Hetzner S3 (source reads) + AWS S3 eu-central-1 (offsite writes).
# Exact-name AND wildcard FQDNs on BOTH providers (A1 lesson: SDK virtual-hosted addressing is
# black-holed by exact-only matchName; AWS defaults TO virtual-hosted = bucket.s3.eu-central-1…).
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kms-offsite
  namespace: data
  labels: {app.kubernetes.io/name: kms-offsite}
automountServiceAccountToken: false
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: kms-offsite-egress
  namespace: data
spec:
  podSelector:
    matchLabels: {app.kubernetes.io/name: kms-offsite}
  policyTypes: [Egress]
  egress:
    - to:
        - namespaceSelector:
            matchLabels: {kubernetes.io/metadata.name: kube-system}
          podSelector:
            matchLabels: {k8s-app: kube-dns}
      ports:
        - {port: 53, protocol: UDP}
        - {port: 53, protocol: TCP}
---
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: kms-offsite-egress-s3
  namespace: data
spec:
  endpointSelector:
    matchLabels: {app.kubernetes.io/name: kms-offsite}
  egress:
    - toEndpoints:
        - matchLabels:
            k8s:io.kubernetes.pod.namespace: kube-system
            k8s-app: kube-dns
      toPorts:
        - ports: [{port: "53", protocol: ANY}]
          rules:
            dns: [{matchPattern: "*"}]
    - toFQDNs:
        - matchName: fsn1.your-objectstorage.com
        - matchPattern: "*.fsn1.your-objectstorage.com"
        - matchName: s3.eu-central-1.amazonaws.com
        - matchPattern: "*.s3.eu-central-1.amazonaws.com"
      toPorts:
        - ports: [{port: "443", protocol: TCP}]
```

Repo-wide FQDN inventory (all :443 unless noted): `api.tatum.io`, `api.bitget.com`, `ws.bitget.com`, `swap.io`, `dev.drsw.io`, `dev-api.drsw.io`, `dev-admin.drsw.io`, Hetzner + AWS object-storage pairs, `hc-ping.com`, `ntfy.sh`, `smtp.maileroo.com`. `toEntities` used only for `kube-apiserver` (with kubeProxyReplacement a vanilla NP cannot target the apiserver — no pod selector behind `default/kubernetes`) and `host`/`remote-node` scrape paths. Vanilla-NP fallback where FQDNs unavailable: `ipBlock 0.0.0.0/0 except [RFC1918 + 169.254/16]`.

**Agent-namespace FQDN block for caesar's k8s mode** (derived): `api.anthropic.com`, `github.com`, `api.github.com`, `*.githubusercontent.com` — wildcard companions mandatory (CDN splitting). "No cluster-internal access" cannot be literal: the kube-dns:53 L7 hop is the irreducible floor in all 43 CNPs. Omit apiserver egress; SA `automountServiceAccountToken: false`.

### Static guards + live tests (two-layer doctrine)

- `apps-netpol-egress-guard.sh` — asserts against the **rendered** apps kustomization: :5432 only to `cnpg.io/cluster=redlight-db`, :4222 only to NATS, :4000 only to the KMS signers; no CNP opens those ports via toEntities/toCIDR/toFQDNs.
- Floor guards — `enableDefaultDeny` must be an object; `matchPattern` stays `"*"`; gap CNPs keep `enableDefaultDeny: false` ("true beats false" — an enforcing gap policy destroys the floor's rollback); no `world`/`cluster` entities in floors.
- `tests/contract/network_policy_test.go` — live busybox probe pods; block proven by "download timed out" + 4.5s silent-drop floor; port-isolation negative test.

### New-namespace convention

Namespace manifest with `app.kubernetes.io/*` labels + PSA triple (house: enforce baseline / audit+warn restricted; web/admin use enforce restricted) + environment label + `default-deny: enabled` label; a `cilium-egress.yaml` with the DNS idiom; a floor CNP if egress deny wanted (Kyverno gives nothing on egress); Flux Kustomization + bootstrap apply line; Kyverno gates (several match explicit namespace lists — new namespaces silently exempt until added).

---

## 3. Tetragon

### Install (`kubernetes/tetragon/release.yaml`)

HelmRelease chart **1.7.0** (from the cilium HelmRepository), ns kube-system, every node incl. CPs. Talos-specific: `extraHostPathMounts: [{name: sys-kernel-tracing, mountPath: /sys/kernel/tracing}]`; BTF at `/sys/kernel/btf/vmlinux`. `enablePolicyFilter: true` (required for namespaced policies), `enableProcessCred/Ns: true`, metrics with `metricsLabelFilter: namespace,workload,node_name` (drops pod+binary cardinality), `exportAllowList: {"namespace":["data"]}` (**Loki export bounded to ns `data` — a new namespace produces zero events until this is edited**, attended restart required). Agent writes `tetragon.log`; existing Alloy tails it (same pattern as hubble flowlog). Flux: deliberately isolated Kustomization (an eBPF/Talos failure must not wedge `infrastructure-config` dependents); `tetragon-policies` dependsOn tetragon **only** (not data — "gating runtime-security OBSERVE on data-stack HEALTH is wrong").

### The four TracingPolicies (all `TracingPolicyNamespaced`, ns `data`, same 5-workload podSelector)

| Policy | Hooks | Watches |
|---|---|---|
| `sensitive-file-reads` | `security_file_permission`, `security_mmap_file` | reads under `/var/run/secrets*`, `/home/node/.tatumrc`; postfix `.key`/`.pem` anywhere |
| `process-exec-shell-miner` | `security_bprm_check` | exec of shells (`/bin/sh`, `/bin/bash`, `busybox`, …); miner binaries postfix |
| `privilege-escalation` | `security_capset`, `create_user_ns`, `__sys_set*id` | cap raise to non-zero; unprivileged userns; set*id to 0 |
| `egress-forensic` | `tcp_connect` | connections NotDAddr RFC1918+loopback (world egress), `rateLimit: 1m` scope process — **process attribution that NetworkPolicy cannot see** |

**Enforcement: none — every action is `Post` (observe).** Full example (`04-network-egress.yaml`):

```yaml
apiVersion: cilium.io/v1alpha1
kind: TracingPolicyNamespaced
metadata: {name: egress-forensic, namespace: data}
spec:
  podSelector:
    matchExpressions:
      - {key: app.kubernetes.io/name, operator: In,
         values: [tatum-kms, tatum-kms-eth, swap-worker, iam-http, iam-nats]}
  kprobes:
    - call: "tcp_connect"
      syscall: false
      args: [{index: 0, type: "sock"}]
      selectors:
        - matchArgs:
            - index: 0
              operator: "NotDAddr"
              values: ["127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"]
          matchActions:
            - {action: Post, rateLimit: "1m", rateLimitScope: "process"}
```

### The guard (`scripts/tetragon-tracingpolicy-guard.sh`) — hard-won landmines encoded

- (a) all actions must be `Post` (Stage 0/1 observe-only; header anticipates relaxing for Stage-2 enforce, scoped).
- (a2) no `In`/`NotIn` in ArgSelector (v1.7.0 CRD enum lacks them — apply would reject).
- (a3) capability spelling is `DAC_OVERRIDE`, never `CAP_DAC_OVERRIDE` — wrong spelling → agent `state=load_error`, whole policy silently dropped (found live 2026-06-18).
- (h) every `security_capset` selector carries `matchNamespaces: Pid NotIn ["host_ns"]` — the **runc exclusion**; without it host runc events attribute into the scope (~5000 events at soak; "would have killed runc cluster-wide under enforce").
- Stage-2 primitives documented but unused: `Override` with `argError: -13` (synchronous `-EACCES` via fmod_ret on `security_*` hooks — works on Talos without `CONFIG_BPF_KPROBE_OVERRIDE`) and `Sigkill` (async); enforce recipe adds `matchPIDs NotIn [1] followForks isNamespacePID` so container PID 1 is never blocked.
- Negative finding: a `setns`/`unshare` container-escape policy was authored, live-tested, and **deliberately not shipped** — seccomp `RuntimeDefault` returns EPERM before the syscall reaches the kprobe (policy never fires).

### Testing conventions

Kyverno: `kyverno test` with 91 pass/fail fixtures + "Invalid Policy" grep. Terraform: conftest plan-pass/fail JSONs + 11 tofu tests. Go: build-tagged smoke/contract/static suites. ~40 of 136 scripts are `*-guard.sh`/`*-static-validate.sh` structural linters — the repo's dominant safety mechanism.

---

## Transfer notes for caesar

- **Compose mode:** Tetragon runs standalone on the VM (container-runtime aware, no k8s); the four policy *shapes* (file-reads, exec-watch, privilege, egress-forensic) transfer as host TracingPolicies scoped to agent containers; the runc exclusion + spelling landmines still apply. Cilium does NOT transfer — Docker-mode egress = per-run default-deny network + filtering proxy/nftables with DNS pinning.
- **k8s mode:** copy the kms-offsite triple (SA/NP/CNP) + a floor CNP with `enableDefaultDeny: {ingress: true, egress: true}` **from day one** (greenfield namespace — the additive→enforce staging existed only to retrofit live traffic); ResourceQuota/LimitRange must be written fresh (no precedent); PSA `enforce: restricted`.
- Misc: LAN pull-through registry mirror at `http://10.10.10.24:5000-5003` (plaintext, "bitnet LAN CT 114") configured as fallback on every redlight node — donor-side observation only.
