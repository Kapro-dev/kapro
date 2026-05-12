# Build stage
FROM golang:1.22-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" \
    -o /workspace/kapro-operator \
    ./cmd/operator

# Runtime stage — distroless, non-root
FROM gcr.io/distroless/static:nonroot

LABEL org.opencontainers.image.title="kapro-operator" \
      org.opencontainers.image.description="Kapro control plane operator — manages Release, Promotion, BatchRun, Approval" \
      org.opencontainers.image.source="https://github.com/Kapro-dev/kapro" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=builder /workspace/kapro-operator /kapro-operator

USER 65532:65532

ENTRYPOINT ["/kapro-operator"]
