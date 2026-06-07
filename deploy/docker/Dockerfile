FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o levara-server ./cmd/server/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o levara ./cmd/cli/

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

RUN mkdir -p data

COPY --from=builder /app/levara-server .
COPY --from=builder /app/levara /usr/local/bin/levara

EXPOSE 8080 50051

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
  CMD wget -qO- http://localhost:8080/health || exit 1

CMD ["./levara-server", "-standalone=true", "-dim=768"]
