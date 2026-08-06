FROM golang:1.26-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
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
