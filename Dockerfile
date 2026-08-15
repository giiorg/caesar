# The ui stage builds the web client. It is first so the Go stage can copy the
# bundle in: //go:embed cannot reach outside its own package, so vite writes
# straight into the Go package that embeds it (web/vite.config.ts's
# build.outDir is ../backend/internal/webui/dist, which lands at
# /backend/internal/webui/dist from this stage's /web).
FROM node:24.19.0-alpine AS ui
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
ARG COMMIT=none
# The commit stamp is baked into the bundle here so that §8's image assertion
# can compare it against the SHA the workflow holds independently. Reading a
# build stamp out of the candidate's own index.html and confirming the asset it
# names exists would be satisfied perfectly by a stale-but-consistent build —
# which is the exact thing being tested for.
#
# WHERE IT LANDS, and how to check: CAESAR_COMMIT reaches the bundle as
# `<meta name="caesar-commit" content="…">` in dist/index.html, injected by
# vite.config.ts's caesar-commit-stamp plugin. §8 reads that tag off a running
# container and compares it to the workflow's SHA.
#
# That sentence was FALSE when it was first written, which is why it now names
# the mechanism instead of asserting the outcome. MEASURED 2026-08-15: the
# original plumbing relied on vite's `define: { __CAESAR_COMMIT__: … }`, but
# nothing in web/src referenced that identifier — it was only DECLARED, in
# src/vite-env.d.ts — so the substitution ran at zero call sites and
# `grep -rl DEADBEEFCAFE0001` over the whole built dist exited 1. §8 would have
# been reading an oracle nothing wrote. backend/internal/webui's
# TestServedIndexCarriesTheBuildStamp is now the guard, and it asserts exact
# equality with $CAESAR_COMMIT whenever that variable is set.
#
# Never npx: MEASURED on this project, npx and `npm exec --` both fall back to
# the public registry when the binary is missing locally and then exit 0 having
# done nothing. Inside this stage npm ci has just run, so the binary is there —
# and the rule is absolute precisely so that no reader has to make that
# judgement per call site.
RUN CAESAR_COMMIT=${COMMIT} ./node_modules/.bin/vite build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# The bundle, from the ui stage. .dockerignore excludes
# backend/internal/webui/dist from the build context, so a developer's stale
# local dist can never reach the image — this COPY is the only way in.
COPY --from=ui /backend/internal/webui/dist ./internal/webui/dist
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
# GOTAGS is an ARG rather than a literal -tags embedui for one reason: §8 builds
# a deliberately UI-less image with `--build-arg GOTAGS=` to watch the release
# assertion go red. A build arg that is never DECLARED is silently ignored —
# docker emits a warning nobody reads, the literal stands, the "no-UI" image
# serves the UI, and the mutant reports success while proving nothing. ARG scope
# is per-stage, so it has to be declared HERE, in the stage that consumes it,
# not only in the ui stage above.
#
# The tag is carried by DEFAULT rather than opted into: a build that forgets to
# opt out is the safe direction, and internal/webui's tagless twin answers a
# typed 501 rather than faking a UI for the lanes that deliberately opt out.
ARG GOTAGS=embedui
RUN CGO_ENABLED=0 go build -trimpath -tags "${GOTAGS}" \
    -ldflags "-s -w \
      -X github.com/giiorg/caesar/backend/internal/platform/buildinfo.Version=${VERSION} \
      -X github.com/giiorg/caesar/backend/internal/platform/buildinfo.Commit=${COMMIT} \
      -X github.com/giiorg/caesar/backend/internal/platform/buildinfo.Date=${DATE}" \
    -o /out/caesar ./cmd/caesar

# The git stage is the runner's stage/extract image: caesar's own binary plus
# a real git. It is not the last stage, so a bare `docker build .` still
# produces the control-plane runtime image.
FROM alpine:3.22 AS git
RUN apk add --no-cache git
COPY --from=build /out/caesar /usr/local/bin/caesar
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/caesar"]

# The fakecli stage is CI's agent: a scripted stand-in for a real harness CLI,
# speaking the same filesystem contract the operator-built images do
# (docker/harness/). It lives in this Dockerfile rather than beside them so the
# existing runner CI lane builds it with `task image:build` and no workflow
# change — and, like the git stage, it is deliberately not the last stage.
FROM alpine:3.22 AS fakecli
RUN apk add --no-cache jq
COPY docker/fakecli.sh /usr/local/bin/fakecli
RUN chmod 0755 /usr/local/bin/fakecli
ENTRYPOINT ["/usr/local/bin/fakecli"]

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/caesar /usr/local/bin/caesar
ENTRYPOINT ["/usr/local/bin/caesar"]
CMD ["serve", "-config", "/etc/caesar/config.json"]
