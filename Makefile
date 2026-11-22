GOBIN := $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

.PHONY: tools generate check-generated build test lint run dev-db

tools: ## install code generators and linter
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
	go install github.com/a-h/templ/cmd/templ@v0.3.1118
	go install github.com/pressly/goose/v3/cmd/goose@v3.30.1
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0

generate: ## sqlc + templ
	sqlc generate
	templ generate

check-generated: generate ## fail if generated code is out of date
	git diff --exit-code -- internal/db '*_templ.go'

build:
	CGO_ENABLED=0 go build -trimpath -o bin/server ./cmd/server

test: ## needs Docker (testcontainers starts postgres:17)
	go test -race ./...

lint:
	golangci-lint run ./...

dev-db: ## local postgres on :5433 for `make run`
	docker run -d --name mealplanner-dev-db -e POSTGRES_PASSWORD=dev -e POSTGRES_DB=mealplanner -p 5433:5432 postgres:17-alpine

run: ## run against dev-db; reads .env if present
	set -a; [ -f .env ] && . ./.env; set +a; go run ./cmd/server
