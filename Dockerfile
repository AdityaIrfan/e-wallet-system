# ── build stage ───────────────────────────────────────────────────────
FROM golang:1.26.5-alpine AS builder

WORKDIR /app

# Cache dependency downloads separately from source changes
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Generate swagger docs before building
RUN go install github.com/swaggo/swag/cmd/swag@latest && \
    swag init -g cmd/server/main.go -o docs

RUN CGO_ENABLED=0 GOOS=linux go build -o /ewallet ./cmd/server

# ── runtime stage ─────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /ewallet .
COPY --from=builder /app/migrations ./migrations
COPY --from=builder /app/docs ./docs

EXPOSE 8080

CMD ["./ewallet"]