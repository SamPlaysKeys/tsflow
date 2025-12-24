# Multi-stage build for TSFlow with embedded frontend
# Optimized for multi-arch builds (amd64/arm64)

# Frontend build stage
FROM node:20-alpine AS frontend-build

WORKDIR /app/frontend

COPY frontend/package*.json ./
RUN npm ci

COPY frontend/ ./
RUN npm run build

# Backend build stage - compile on native arch, cross-compile for target
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS backend-build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app/backend

RUN apk add --no-cache git ca-certificates

COPY backend/go.mod backend/go.sum ./

# Cache Go modules
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY backend/ ./
COPY --from=frontend-build /app/backend/frontend/dist ./frontend/dist

# Cross-compile with cache mounts for speed
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-w -s" -trimpath -o tsflow ./main.go

# Runtime stage - distroless for minimal image
FROM gcr.io/distroless/static:nonroot

WORKDIR /app

COPY --from=backend-build /app/backend/tsflow .

ENV ENVIRONMENT=production

EXPOSE 8080

USER 65532:65532

ENTRYPOINT ["/app/tsflow"]
