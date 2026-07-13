FROM golang:1.26.5-alpine AS builder

WORKDIR /app

# Bring deps in first so layer cache survives source-only edits.
COPY go.mod go.sum ./
RUN go mod download

# Source — includes docs/ (swaggo-generated) so the swagger UI works
# without a separate copy step.
COPY . .

# CGO disabled keeps the image static; ldflags strip debug info.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o levara ./cmd/server/

FROM alpine:3.21

WORKDIR /app

ENV LEVARA_HTTP_HOST=0.0.0.0 \
    LEVARA_GRPC_HOST=0.0.0.0

RUN addgroup -S levara && adduser -S -G levara -h /app levara \
    && mkdir -p data && chown -R levara:levara /app

COPY --from=builder --chown=levara:levara /app/levara .

USER levara

# 8080 = HTTP API + Swagger UI; 50051 = gRPC (v1 + v2 on the same port).
EXPOSE 8080 50051

# Defaults for environment variables that have hard production
# requirements (see docs/MIGRATION-20.04.md):
#   JWT_SECRET — generate via `openssl rand -hex 32`, persist in secrets store
#   ENV=production — disables /swagger/* exposure
#   REQUIRE_AUTH=true — flips gRPC + HTTP auth from permissive to strict
# These are not baked here; the operator sets them via -e or compose.

CMD ["./levara"]
