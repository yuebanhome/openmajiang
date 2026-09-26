SHELL := /bin/bash
GO ?= go
COMPOSE := docker compose --env-file deploy/.env -f deploy/compose.yaml

.PHONY: help dev web assets test test-contract e2e build image up down migrate backup restore
help:
	@echo 'dev: Go server (web dev is a separate terminal); web: Vite; test: all unit/integration tests; build: both binaries; image: local Docker image'

dev: assets
	$(GO) run ./cmd/openmajiang serve

web:
	cd web && npm run dev

assets:
	cd web && npm ci && npm run build

test: assets
	$(GO) vet ./...
	CGO_ENABLED=1 $(GO) test -race ./...
	cd web && npm run typecheck && npm test
	python3 -m unittest discover -s deploy -p 'test_*.py'

test-contract:
	$(GO) test ./pkg/rulesdk/... ./rules/...

e2e:
	./deploy/smoke.sh
	cd web && npm run test:e2e

build: assets
	CGO_ENABLED=1 $(GO) build -trimpath -o bin/openmajiang ./cmd/openmajiang
	CGO_ENABLED=1 $(GO) build -trimpath -o bin/bot-runner ./cmd/bot-runner

image:
	docker build -t openmajiang:local .

up:
	$(COMPOSE) up -d

down:
	$(COMPOSE) down

migrate:
	$(COMPOSE) run --rm migrate

backup:
	./deploy/backup.sh

restore:
	./deploy/restore.sh "$(BACKUP)"
