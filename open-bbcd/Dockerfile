# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" \
    -o /out/open-bbcd ./cmd/open-bbcd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/open-bbcd /usr/local/bin/open-bbcd
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=3 \
    CMD ["/usr/local/bin/open-bbcd", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/open-bbcd"]
