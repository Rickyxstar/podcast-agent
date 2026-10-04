# syntax=docker/dockerfile:1

# The build stage runs on the host's platform and cross-compiles for the
# target, so building linux/amd64 on Apple Silicon needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/podcast-agent ./cmd/podcast-agent

# Static binary on distroless: CA certificates and tzdata, no shell, runs as
# uid 65532 (matches the Helm chart's securityContext).
FROM gcr.io/distroless/static-debian13:nonroot

# KB_DIR defaults to "kb", relative to this directory.
WORKDIR /app

COPY --from=build /out/podcast-agent /app/podcast-agent
COPY kb ./kb
# Bundled so `docker run <image> run samples/<file>` works without a mount.
COPY samples ./samples

USER nonroot:nonroot

ENTRYPOINT ["/app/podcast-agent"]
CMD ["--help"]
