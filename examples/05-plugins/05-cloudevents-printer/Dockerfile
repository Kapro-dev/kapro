# Multi-stage distroless build for the cloudevents-printer reference subscriber.
#
# Usage:
#   docker build -f examples/05-plugins/05-cloudevents-printer/Dockerfile -t kapro-cloudevents-printer:dev .
#   docker run --rm -p 8080:8080 kapro-cloudevents-printer:dev
#
# Then POST a CloudEvents v1.0 envelope at http://localhost:8080/ and watch
# stdout. The KAPRO_PRINTER_AUTH_HEADER / KAPRO_PRINTER_LISTEN_ADDR env
# vars work the same way they do in-cluster.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath -ldflags="-s -w" \
        -o /out/cloudevents-printer \
        ./examples/05-plugins/05-cloudevents-printer

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/cloudevents-printer /usr/local/bin/cloudevents-printer
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/cloudevents-printer"]
