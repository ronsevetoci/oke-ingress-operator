# syntax=docker/dockerfile:1.7
LABEL org.opencontainers.image.source=https://github.com/ronsevetoci/oke-ingress-operator
FROM --platform=$BUILDPLATFORM golang:1.24 AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ENV CGO_ENABLED=0
WORKDIR /workspace

COPY go.mod ./
COPY go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download || true

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod go mod tidy

RUN --mount=type=cache,target=/go/pkg/mod \
    GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o manager ./

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
