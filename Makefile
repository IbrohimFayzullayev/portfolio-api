.PHONY: help setup db-up db-down db-logs run build tidy generate test test-integration seed

DATABASE_URL ?= postgres://portfolio:portfolio@localhost:5432/portfolio?sslmode=disable

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

setup: db-up tidy ## Start the database and download deps (then run `make run`)
	@echo ""
	@echo "Database is up. Now run:  make run"
	@echo "(migrations apply automatically on startup)"

run: ## Run the API (applies migrations automatically)
	go run ./cmd/api

seed: ## Import the public site's content/*.mdx into PostgreSQL (idempotent)
	go run ./cmd/seed

build: ## Build the API binary into ./bin
	go build -o bin/api ./cmd/api

tidy: ## Download and tidy Go modules
	go mod tidy

db-up: ## Start PostgreSQL via Docker
	docker compose up -d db
	@echo "Waiting for PostgreSQL to be ready..."
	@until docker compose exec -T db pg_isready -U portfolio >/dev/null 2>&1; do sleep 1; done
	@echo "PostgreSQL is ready."

db-down: ## Stop PostgreSQL
	docker compose down

db-logs: ## Tail the database logs
	docker compose logs -f db

generate: ## (Optional) Regenerate internal/db from db/queries with sqlc
	sqlc generate

test: ## Run unit tests only (fast, no database needed)
	go test ./...

test-integration: db-up ## Run unit + integration tests against a throwaway portfolio_test DB
	@echo "Preparing throwaway test database (portfolio_test)..."
	@docker compose exec -T db psql -U portfolio -d postgres -c "DROP DATABASE IF EXISTS portfolio_test;" >/dev/null
	@docker compose exec -T db psql -U portfolio -d postgres -c "CREATE DATABASE portfolio_test;" >/dev/null
	TEST_DATABASE_URL=postgres://portfolio:portfolio@localhost:5432/portfolio_test?sslmode=disable \
		go test ./... -count=1 -v
