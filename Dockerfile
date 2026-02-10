FROM golang:1.25-alpine AS builder

RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

# Build with CGO enabled for SQLite
RUN CGO_ENABLED=1 go build -o favicon . && \
    ls -la /app/favicon

FROM alpine:latest

RUN apk --no-cache add ca-certificates sqlite

RUN addgroup -g 1000 -S favicon && adduser -S favicon -u 1000 -G favicon

WORKDIR /app

# Create data directory for database
RUN mkdir -p ./data && chown favicon:favicon ./data

# Copy and verify the binary
COPY --chown=favicon:favicon --from=builder /app/favicon ./favicon

# Make sure the binary is executable
RUN ls -la /app/ && \
    chmod +x /app/favicon

USER favicon

EXPOSE 80

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost/healthz || exit 1

CMD ["./favicon"]
