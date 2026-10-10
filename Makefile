# Local development uses the Postgres + Redis already running on this machine
# (DBngin: Postgres on 54321, Redis on 6379). Override any of these, e.g.
#   make run DB_URL=postgres://mail:mail@localhost:55432/mail?sslmode=disable
DB_URL         ?= postgres://postgres@localhost:54321/klavio?sslmode=disable
TEST_DB_URL    ?= postgres://postgres@localhost:54321/klavio_test?sslmode=disable
REDIS_URL      ?= redis://localhost:6379/0
TEST_REDIS_URL ?= redis://localhost:6379/15

export DATABASE_URL = $(DB_URL)
export REDIS_URL

.PHONY: migrate run test test-integration docker-deps docker-deps-down web-install web-dev web-test check

migrate:           ## apply migrations
	cd api && go run ./cmd/app -mode=migrate

run:               ## run api + worker on :8080
	cd api && go run ./cmd/app -mode=all

test:              ## Go vet + unit tests
	cd api && go vet ./... && go test ./...

test-integration:  ## Go tests including the ones that need Postgres + Redis
	cd api && TEST_DATABASE_URL="$(TEST_DB_URL)" TEST_REDIS_URL="$(TEST_REDIS_URL)" go test -count=1 ./...

docker-deps:       ## optional: Postgres + Redis in Docker instead (ports 55432 / 56379)
	docker compose --profile docker-deps up -d db redis
	@echo 'Use: make run DB_URL=postgres://mail:mail@localhost:55432/mail?sslmode=disable REDIS_URL=redis://localhost:56379/0'

docker-deps-down:
	docker compose --profile docker-deps down

web-install:
	cd web && npm ci

web-dev:           ## React dev server on :5173
	cd web && npm run dev

web-test:
	cd web && npm run typecheck && npm test

check: test web-test   ## everything CI runs, except Docker and infra
