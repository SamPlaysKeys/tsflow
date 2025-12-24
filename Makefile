.PHONY: dev dev-backend dev-frontend build clean docker help

# Default target
help:
	@echo "TSFlow Build Commands:"
	@echo ""
	@echo "  make dev          - Run both backend and frontend in dev mode"
	@echo "  make dev-backend  - Run backend only (no embedded frontend)"
	@echo "  make dev-frontend - Run frontend dev server with hot reload"
	@echo "  make build        - Build production binary with embedded frontend"
	@echo "  make docker       - Build Docker image"
	@echo "  make clean        - Remove build artifacts"
	@echo ""

# Development: run backend without embedded frontend
dev-backend:
	cd backend && go build -tags exclude_frontend -o tsflow . && ./tsflow

# Development: run frontend with Vite (proxies /api to backend)
dev-frontend:
	cd frontend && npm run dev

# Development: run both (requires two terminals, or use this with &)
dev:
	@echo "Starting backend..."
	@cd backend && go build -tags exclude_frontend -o tsflow . && ./tsflow &
	@sleep 2
	@echo "Starting frontend dev server..."
	@cd frontend && npm run dev

# Production: build single binary with embedded frontend
build:
	@echo "Building frontend..."
	cd frontend && npm run build
	@echo "Building backend with embedded frontend..."
	cd backend && go build -o tsflow .
	@echo "Done! Binary at backend/tsflow ($(shell ls -lh backend/tsflow | awk '{print $$5}'))"

# Docker build
docker:
	docker build -t tsflow .

# Clean build artifacts
clean:
	rm -rf backend/tsflow backend/tsflow-backend
	rm -rf backend/frontend/dist
	rm -rf frontend/dist
