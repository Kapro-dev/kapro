# Multi-stage distroless build for the Argo CD actuator plugin.
#
# Usage:
#   docker build -f examples/05-plugins/00-argocd-actuator/Dockerfile -t argocd-actuator:dev .
#   docker run --rm -p 9090:9090 -v "$KUBECONFIG:/kubeconfig:ro" \
#     -e KUBECONFIG=/kubeconfig argocd-actuator:dev --listen :9090

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath -ldflags="-s -w" \
        -o /out/argocd-actuator \
        ./examples/05-plugins/00-argocd-actuator

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/argocd-actuator /usr/local/bin/argocd-actuator
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/argocd-actuator"]
