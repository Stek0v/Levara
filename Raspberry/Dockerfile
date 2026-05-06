FROM golang:1.23-alpine AS builder

ARG TARGETARCH=arm64

WORKDIR /build

# Cache dependencies
COPY Levara-legacy/go.mod Levara-legacy/go.sum ./
RUN go mod download

# Copy source
COPY Levara-legacy/ .

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o cognevra ./cmd/server/

# --- Runtime ---
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

COPY --from=builder /build/cognevra /usr/local/bin/cognevra

# Create non-root user
RUN adduser -D -H -s /sbin/nologin cognevra

VOLUME /data
EXPOSE 8080

ENV DB_PROVIDER=sqlite \
    DB_PATH=/data/cognevra.db \
    LOG_LEVEL=INFO

USER cognevra

ENTRYPOINT ["cognevra"]
CMD ["-standalone=true", "-dim=768", "-shards=1", "-port=8080", "-grpc-port=0", "-data-dir=/data"]
