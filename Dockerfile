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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/caesar /usr/local/bin/caesar
ENTRYPOINT ["/usr/local/bin/caesar"]
CMD ["serve", "-config", "/etc/caesar/config.json"]
