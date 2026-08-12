# syntax=docker/dockerfile:1

# Build stage — TARGETOS/TARGETARCH are populated automatically by BuildKit,
# including under multi-platform builds (docker buildx build --platform
# linux/amd64,linux/arm64 ...). A plain `docker compose up --build` cross-
# compiles for nothing extra: TARGETOS/TARGETARCH default to the host's own
# platform, so each machine just gets its native-arch binary.
FROM golang:1.26-alpine AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# Runtime stage — minimal, non-root.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && \
    addgroup -S app && adduser -S app -G app
COPY --from=builder /out/server /usr/local/bin/server
USER app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/server"]
