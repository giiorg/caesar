# Go style survey — bitnet-backend-go + arb-lab (the user's current Go taste)

*Research artifact, 2026-08-01. Produced by a read-only survey agent. Both repos are the same architecture at two scales; nearly every difference is a documented decision, not drift.*

## Repo A — bitnet-backend-go (`/Users/sofo/Projects/bitnet/repos/bitnet/bitnet-backend-go`)

4,508 Go files, 508k non-test + 372k test LOC (42% test). Production fintech backend replacing a NestJS monolith. `go 1.26.0` / `toolchain go1.26.5`; Go 1.24+ `tool` directives (gqlgen, golangci-lint, golines, sqlc, goimports). ~55 direct deps: pgx/v5, shopspring/decimal, gqlgen, nats.go (+embedded server for tests), otel ×12 modules, go-redis + miniredis, aws-sdk-v2, OPA, webauthn/jwt/otp, goose, testcontainers-go, testify, rapid, goleak, coder/websocket, anthropic-sdk-go, backoff, uuid, x/{sync,text,time}.

**Layout** (documented in `docs/standards/repository-layout.md`): `internal/boundary/<context>/` — 12 DDD bounded contexts, each `domain/{aggregate,valueobject,service}` + `application/{aggregate,ports,dto,errors}` + `infrastructure/{driven,driving}` · `internal/engine/` — technical subsystems, no DDD layering · `internal/platform/` — 60+ shared-kernel packages, no business logic · `internal/admin-projections/` — CQRS read side · `internal/app/{admin-api,customer-api,admin-ai}/` — GraphQL gateway composition roots · `internal/testsupport/` · `pkg/domain/{identity,errclass}` the only exported surface. Hexagonal named explicitly: `application/ports/` interfaces, `infrastructure/driven` outbound adapters, `driving` inbound with a closed vocabulary (natshandler/natssubscriber/webhook, per ADR). 15 public binaries in `cmd/`; internal workers in per-context `cmd/`.

**Conventions:** errors `fmt.Errorf("doing thing: %w", err)` + `errors.Is/As`; application layer translates to `AppError` with machine-readable codes (ADR-029 envelope). Caller identity arrives as command-struct fields, **never** `FromContext()` in core services (ADR-028); repos extract `pgx.Tx` from context via BC-local `dbFromCtx` (ADR-027). `log/slog` only, constructed via observability helpers, injected — zerolog/zap/logrus depguard-denied. Config: env + per-service `.env` + AWS AppConfig. **DI manual**: per-BC `module.go` composition root assembling a decorator chain — `observability → authz → masker → validation → notifier → audit → core` (audit innermost, ADR-020); wire/fx depguard-denied.

**Persistence:** pgx/v5 + pgxpool; Go owns 13 named schemas (legacy NestJS owns `public.*` — no DDL there). sqlc for simple CRUD; hand-written repos for complex queries. **Atlas** authoring + **goose** embedded runtime; per-context migration dirs + checked-in `migrations.sum` + forward-only-down test. Transactional outbox per BC → NATS JetStream `{context}.events.{aggregate}.{tail}`; consumers use inbox pattern.

**HTTP/API:** stdlib ServeMux (chi/gin/echo depguard-denied with the alternative named in the denial message). External API GraphQL via gqlgen (schema-first, two gateways). Internal service-to-service = NATS RPC wrapped in typed SDK packages (`internal/platform/*sdk`). Gateways may not import `internal/boundary/` — enforced by an AST test in `cmd/customer-api/main_test.go`. Cross-cutting via decorator chain, not HTTP middleware.

**Testing:** 1,724 test files. testify (1,031 files), table-driven, testcontainers (real Postgres/NATS/MinIO), rapid property tests, goleak. Distinctive: **architecture-as-test** — AST-walking checks assert hexagonal layering, no cross-BC imports, no gateway→boundary imports, audit/authz completeness, wire parity, enum exhaustiveness, statemachine coverage; plus a `tools/check*` family (checkadr, checklogger, checkgqlrules, checkclockwiring, checkauditmetadata, checkmockresiduals, checkcharters). Per-diff testing obligations via `task test:obligations`.

**Tooling:** 57KB Taskfile (~100 targets; air hot reload; `task verify` = build+vet+lint+contract+race+govulncheck+drift). 21KB `.golangci.yml` on **`default: all`** with 12 justified disables; depguard encodes the deny-list. 190 ADRs, 57 standards docs, 38KB CLAUDE.md. Commits: `<type>(<scope>): <description>`, one sentence, no body, no AI attribution.

## Repo B — arb-lab (`/Users/sofo/Projects/misc/arb-lab`)

~200 Go files, 55k non-test + 59k test LOC (**52% test**). Funding-rate arbitrage engine, spec-first against a normative `docs/build-spec.md`. `go 1.26.0` / `toolchain go1.26.5` (split commented in go.mod; CI uses `go-version-file`). **Two direct dependencies total**: `pgregory.net/rapid`, `coder/websocket`. Everything else hand-built under `internal/platform/` (apperror, boot, clock, config, events, fixedpoint, ids, observability, outbox, ratelimit). Nested `tools/lint/go.mod` isolates `golang.org/x/tools`; deliberately **no** `go.work`.

**Layout:** bounded contexts at the **top level** of `internal/` (marketdata, strategy, oms, risk, treasury, audit) as peers of `platform/`, `engine/` (capture, venue, replay, state, dispatch — technical, unlayered), `app/` (composition roots). CLAUDE.md records that a `boundary/` grouping segment was **rejected on 2026-07-26** ("no structural gate keyed on it → zero enforcement value"). One CLI binary; hand-rolled `dispatch(args, stdout, stderr) int`, `EX_USAGE` 64.

**Conventions:** two-axis error taxonomy — `Class` (what went wrong; drives backoff + metric labels) × `Effect` (did the side effect happen; drives order lifecycle), deliberately not collapsed; unexported fields so an `Error` always carries validated classification; unknown → `ClassInternal`/`EffectUnknown` (forgetting is safe). **Time never ambient**: `platform/clock` seam enforced by a custom analyzer; `observability.Logger` wraps slog *solely* because slog's own methods call `time.Now()` — it builds `slog.Record` from `clock.Now`. Config: strict-decoded JSON (unknown keys, duplicate keys, trailing data, untagged exported fields all rejected); hot-reload opt-in per field via `config:"hot"` tag. `platform/boot` runs ordered phases returning a four-valued `Posture` ladder ("alive and refusing" is a real outcome a `func() error` cannot express).

**Persistence:** none yet (planned Postgres; no driver chosen). Durable state = append-only capture archive (custom binary codec, manifests, flock, gap detection, fuzz-tested decoder).

**Testing (the repo's signature):** **zero testify** — stdlib only. White-box `_internal_test.go` alongside black-box. 14 packages with rapid property tests, **pinned seed** (`RAPID_SEED=20260726`) for reproducible CI + a nightly randomized explorer target. Two fuzz targets with committed corpora. Differential oracles (fixedpoint vs `math/big`; ids vs independent base32). Mutation harness `tools/mutate.sh` reporting **six verdicts** (`caught`, `SURVIVED`, `NO-OP!!`, `NOBUILD!!`, `TIMEOUT!!`, `PANIC!!`) because five things had masqueraded as kills. Compile-failure fixtures (`testdata/typesafety/`) asserting `Money + Quantity` does not build.

**Tooling:** 4KB self-documenting Makefile; `make ci` = fmt-check, vet, custom gates, race tests, both modules. **No golangci-lint** — six hand-written `go/analysis` analyzers with fixture trees: `nofloat` (type-based, `//nofloat:allow <reason>` escape), `notimenow`, `kernelpurity` (platform imports stdlib+platform only), `hexboundary`, `exhaustiveswitch` (a `default:` does not excuse a missing member), `singlewriter` (only oms may import the OMS write path). One GitHub Actions job running `make ci`.

## Synthesis — the user's Go style in 10 points

1. **Hexagonal + DDD as default skeleton, enforced mechanically** (AST tests / analyzers), never by review alone. Ship the layering *and* its gate.
2. **Stdlib-first with an explicit deny-list** — slog, ServeMux; no ORMs, DI containers, routers, alt loggers; denials name the sanctioned alternative.
3. **Manual composition roots**; cross-cutting = decorator chain in fixed documented order, not middleware.
4. **Money never floats**; exceptions enumerated per-call-site (decimal lib or dimensional types that don't cross-type-check).
5. **Time is injected** (`platform/clock`), analyzer-enforced.
6. **Default-deny as stated principle** — unknown config rejected; hot-reload opt-in; unclassified error reads as "may have applied"; "pick the direction that's right when somebody forgets."
7. **Tests are half the codebase**; the bar is *bite*: table-driven + property (pinned seed + nightly random) + fuzz (committed corpora) + differential oracles + mutation + compile-failure fixtures; integration on real deps (testcontainers), never DB mocks.
8. **Package doc comments carry the reasoning** — what the package deliberately does *not* do, which alternatives were rejected.
9. **Decisions written down**: ADRs, standards docs, normative build-spec; deviations marked and swept.
10. **Commits**: `<type>(<scope>): <description>`, one sentence, no body, no AI attribution; write and commit as separate steps.

## Open splits to settle for caesar

- `boundary/` segment (bitnet) vs contexts at top of `internal/` (arb-lab; newer decision — recommended).
- testify (bitnet, 1031 files) vs stdlib-only (arb-lab, newer).
- golangci-lint `default: all` vs bespoke analyzers — complementary; caesar likely both.
- Taskfile (57KB) vs Makefile (4KB).
- Config: env+struct tags (+remote) vs strict JSON files with hot-reload tags.
- Error model: `AppError` code envelope (API-facing) vs two-axis Class/Effect (execution-safety-facing) — caesar is both an API and an engine; likely two-axis internally + envelope at the API boundary.
- API surface: bitnet is GraphQL external + NATS RPC internal; arb-lab serves nothing — **neither is precedent for caesar's REST+OpenAPI choice; that decision was made on its own merits.**
- Dependency appetite: ~55 direct deps vs 2 — caesar aims low (stdlib-first) without re-implementing the world; each dep justified.
- arb-lab's nested `tools/lint` module (no go.work) is the pattern to copy if caesar grows custom analyzers.
