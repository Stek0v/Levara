FROM golang:1.26-alpine AS builder

WORKDIR /app

# Bring deps in first so layer cache survives source-only edits.
COPY go.mod go.sum ./
RUN go mod download

# Source — includes docs/ (swaggo-generated) so the swagger UI works
# without a separate copy step.
COPY . .

# CGO disabled keeps the image static; ldflags strip debug info.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o levara ./cmd/server/main.go

FROM alpine:latest

WORKDIR /app

RUN mkdir -p data

COPY --from=builder /app/levara .

# 8080 = HTTP API + Swagger UI; 50051 = gRPC (v1 + v2 on the same port).
EXPOSE 8080 50051

# Defaults for environment variables that have hard production
# requirements (see docs/MIGRATION-20.04.md):
#   JWT_SECRET — generate via `openssl rand -hex 32`, persist in secrets store
#   ENV=production — disables /swagger/* exposure
#   REQUIRE_AUTH=true — flips gRPC + HTTP auth from permissive to strict
# These are not baked here; the operator sets them via -e or compose.

CMD ["./levara"]
