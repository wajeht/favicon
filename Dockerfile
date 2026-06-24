FROM golang:1.26-alpine@sha256:3ad57304ad93bbec8548a0437ad9e06a455660655d9af011d58b993f6f615648 AS builder

RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

# Build with CGO enabled for SQLite
RUN CGO_ENABLED=1 go build -o favicon . && \
    ls -la /app/favicon

FROM alpine:latest@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

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
