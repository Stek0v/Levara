FROM golang:1.26.5-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o levara-server ./cmd/server/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o levara ./cmd/cli/

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

ENV LEVARA_HTTP_HOST=0.0.0.0 \
    LEVARA_GRPC_HOST=0.0.0.0

RUN addgroup -S levara && adduser -S -G levara -h /app levara \
    && mkdir -p data && chown -R levara:levara /app

COPY --from=builder --chown=levara:levara /app/levara-server .
COPY --from=builder --chown=levara:levara /app/levara /usr/local/bin/levara

USER levara

EXPOSE 8080 50051

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
  CMD wget -qO- http://localhost:8080/health || exit 1

CMD ["./levara-server", "-standalone=true", "-dim=768"]
