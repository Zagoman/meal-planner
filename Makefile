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

# ---------------------------------------------------------------------------
# Deploy (package D): see deploy/README.md
# ---------------------------------------------------------------------------
IMAGE       ?= ghcr.io/zagoman/meal-planner
PLATFORM    ?= linux/amd64
SHA         ?= $(shell git rev-parse --short=12 HEAD)
DEPLOY_HOST ?= 192.168.0.31
DEPLOY_USER ?= lucs
STACK_DIR   ?= /opt/stacks/mealplanner
DOMAIN      ?= meals.liluhome.dk
HOMELAB     ?= $(HOME)/dev/homelab-server

.PHONY: image push deploy deploy-only rollback homelab-sync stack-config smoke

image: ## build $(IMAGE):$(SHA) and :latest for $(PLATFORM)
	@if [ -z "$(ALLOW_DIRTY)" ] && [ -n "$$(git status --porcelain)" ]; then \
		echo "working tree is dirty: commit first (or ALLOW_DIRTY=1)"; exit 1; fi
	docker buildx build --platform $(PLATFORM) --build-arg REVISION=$(SHA) \
		-t $(IMAGE):$(SHA) -t $(IMAGE):latest --load .

push: image ## push both tags to GHCR (needs `docker login ghcr.io`)
	docker push $(IMAGE):$(SHA)
	docker push $(IMAGE):latest

deploy: push deploy-only ## build, push, pull + up on the server, wait for /healthz

deploy-only: ## pull + up on the server without building (expects :latest == SHA)
	deploy/scripts/deploy.sh $(DEPLOY_USER)@$(DEPLOY_HOST) $(STACK_DIR) $(SHA) $(DOMAIN)

rollback: ## make rollback SHA=<older sha>: point :latest at that image and deploy it
	@if [ "$(origin SHA)" != "command line" ]; then echo "usage: make rollback SHA=<12-char sha>"; exit 1; fi
	docker buildx imagetools create -t $(IMAGE):latest $(IMAGE):$(SHA)
	deploy/scripts/deploy.sh $(DEPLOY_USER)@$(DEPLOY_HOST) $(STACK_DIR) $(SHA) $(DOMAIN)

homelab-sync: ## copy deploy/stack/* into the homelab checkout's mealplanner/ (commit there)
	@[ -d "$(HOMELAB)/ansible" ] || { echo "no homelab repo at $(HOMELAB)"; exit 1; }
	mkdir -p "$(HOMELAB)/mealplanner"
	cp deploy/stack/compose.yml deploy/stack/backup.sh deploy/stack/restore.sh "$(HOMELAB)/mealplanner/"
	git -C "$(HOMELAB)" status --short mealplanner

stack-config: ## validate deploy/stack/compose.yml with dummy values
	APPDATA_DIR=/tmp/appdata POSTGRES_PASSWORD=x SESSION_SECRET=x DATABASE_URL=x \
		docker compose -f deploy/stack/compose.yml config --quiet && echo "compose config ok"

smoke: ## local end-to-end: stack from $(IMAGE):$(SHA), /healthz, backup + restore round-trip
	deploy/scripts/smoke.sh $(IMAGE):$(SHA)
