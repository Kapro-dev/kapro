# Multi-stage distroless build for the Flux actuator plugin.
#
# Usage:
#   docker build -f examples/plugins/flux-actuator/Dockerfile -t flux-actuator:dev .

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath -ldflags="-s -w" \
        -o /out/flux-actuator \
        ./examples/plugins/flux-actuator

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/flux-actuator /usr/local/bin/flux-actuator
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/flux-actuator"]
