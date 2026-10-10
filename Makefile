DB_URL ?= postgres://mail:mail@localhost:5432/mail?sslmode=disable

.PHONY: db migrate run test test-integration web-install web-dev web-test check

db:                ## start local Postgres
	docker compose up -d db

migrate:           ## apply migrations to local Postgres
	cd api && DATABASE_URL="$(DB_URL)" go run ./cmd/app -mode=migrate

run:               ## run api + worker locally
	cd api && DATABASE_URL="$(DB_URL)" go run ./cmd/app -mode=all

test:              ## Go unit tests
	cd api && go vet ./... && go test ./...

test-integration:  ## Go tests including the ones that need Postgres
	cd api && TEST_DATABASE_URL="$(DB_URL)" go test -count=1 ./...

web-install:
	cd web && npm ci

web-dev:           ## React dev server on :5173
	cd web && npm run dev

web-test:
	cd web && npm run typecheck && npm test

check: test web-test   ## everything CI runs, except Docker and infra
