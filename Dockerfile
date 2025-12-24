# Multi-stage build for TSFlow with embedded frontend

# Frontend build stage
FROM node:20-alpine AS frontend-build

WORKDIR /app/frontend

COPY frontend/package*.json ./
RUN npm ci

COPY frontend/ ./
# Build outputs to ../backend/frontend/dist (relative to frontend/)
RUN npm run build

# Backend build stage
FROM golang:1.25-alpine AS backend-build

WORKDIR /app/backend

RUN apk add --no-cache git

COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./
# Copy built frontend into backend/frontend/dist for embedding
COPY --from=frontend-build /app/backend/frontend/dist ./frontend/dist

# Build with embedded frontend
RUN CGO_ENABLED=0 GOOS=linux go build -o tsflow ./main.go

# Runtime stage - single binary, no separate frontend files needed
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /app

# Only copy the binary - frontend is embedded
COPY --from=backend-build /app/backend/tsflow ./

# Set default environment to production
ENV ENVIRONMENT=production

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

CMD ["./tsflow"]
