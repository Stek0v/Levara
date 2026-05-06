FROM golang:1.23-alpine AS builder

ARG TARGETARCH=arm64

WORKDIR /build

# Cache dependencies
COPY Levara/go.mod Levara/go.sum ./
RUN go mod download

# Copy source
COPY Levara/ .

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o levara ./cmd/server/

# --- Runtime ---
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

COPY --from=builder /build/levara /usr/local/bin/levara

# Create non-root user
RUN adduser -D -H -s /sbin/nologin levara

VOLUME /data
EXPOSE 8080

ENV DB_PROVIDER=sqlite \
    DB_PATH=/data/levara.db \
    LOG_LEVEL=INFO

USER levara

ENTRYPOINT ["levara"]
CMD ["-standalone=true", "-dim=768", "-shards=1", "-port=8080", "-grpc-port=0", "-data-dir=/data"]
