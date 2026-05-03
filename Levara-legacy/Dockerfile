FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o cognevra ./cmd/server/main.go

FROM alpine:latest

WORKDIR /app

RUN mkdir -p data

COPY --from=builder /app/cognevra .

EXPOSE 8080

CMD ["./cognevra"]