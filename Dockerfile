# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY *.go ./

# Build binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o business2api .

# Runtime stage
FROM alpine:latest

WORKDIR /app

# Install runtime dependencies: ca-certificates, tzdata, nodejs, npm, chromium
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    nodejs \
    npm \
    chromium \
    nss \
    freetype \
    harfbuzz \
    ttf-freefont \
    font-noto-cjk

# Set Puppeteer to use system Chromium
ENV PUPPETEER_SKIP_CHROMIUM_DOWNLOAD=true
ENV PUPPETEER_EXECUTABLE_PATH=/usr/bin/chromium-browser

# Copy binary from builder
COPY --from=builder /app/business2api .

# Copy JS registration script and install dependencies
COPY main.js package.json ./
RUN npm install --production && npm cache clean --force

# Copy config template (optional)
COPY config.json.example ./config.json.example

# Create data directory
RUN mkdir -p /app/data

# Environment variables
ENV LISTEN_ADDR=":8000"
ENV DATA_DIR="/app/data"

EXPOSE 8000

ENTRYPOINT ["./business2api"]
