# Harness agent images

The real agent images: `caesar-harness-claude:dev` and `caesar-harness-codex:dev`.

They are **operator-built and never built in CI** — `task image:harness` pulls
the real CLIs from npm, and no CI lane may ever place a live subscription call.
CI's agent is `caesar-fakecli:dev` (`docker/fakecli.sh`, built by
`task image:build`), a scripted stand-in that speaks the same filesystem
contract these images do.

## Which environment the CLIs read

A **brokered** attempt's container receives two forms of the same thing: the
CLI-native pair (`ANTHROPIC_BASE_URL` + `ANTHROPIC_API_KEY` for Claude Code,
`OPENAI_BASE_URL` + `OPENAI_API_KEY` for Codex) and the harness-agnostic
`CAESAR_BROKER_URL` + `CAESAR_BROKER_TOKEN`.

**These images read the CLI-native pair only.** The entrypoints deliberately
never mention `CAESAR_BROKER_*` and never translate one form into the other:
each CLI already consumes its own variables, and a translation layer here would
be a second place the broker contract could drift out of step with `spec.go`.

`CAESAR_BROKER_*` stays in the attempt environment as the contract for
everything that is not one of these two CLIs — Plan 06's isolation probes read
it, and a future non-CLI harness would too.

Under **materialized** custody (the operator's own subscription, spec §2)
neither form of broker variable is set at all: the gateway brokers nothing and
`spec.go` never mints a token. That is why the fixture's
`fakecli:assert-no-broker` directive is a real regression signal rather than a
tautology — a broker token appearing in a materialized attempt would mean the
custody derivation had broken.

## Pins

Each Dockerfile pins its CLI version in an `ARG` so a build is reproducible and
a bump is a reviewable diff. Both were resolved on 2026-08-06 with
`npm view <package> version`. Bumping one is an operator action followed by the
smoke run — the exit markers in `internal/runs/runner/driver.go` are recorded
against a specific CLI version and a bump can invalidate them.
