# syntax=docker/dockerfile:1.7

# ---- Build stage ----
FROM --platform=$BUILDPLATFORM golang:1.24 AS builder
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOFLAGS="-buildvcs=false"
WORKDIR /workspace

# deps first (cache-friendly)
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# source
COPY . .

# build
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /workspace/manager ./

# ---- Runtime stage ----
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]

# OCI labels on the final image
LABEL org.opencontainers.image.source="https://github.com/ronsevetoci/oke-ingress-operator" \
      org.opencontainers.image.title="OKE Ingress Operator" \
      org.opencontainers.image.licenses="Apache-2.0"