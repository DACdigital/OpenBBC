.PHONY: build run test clean migrate-up migrate-down migrate-status migrate-create

BINARY_NAME=open-bbcd
BUILD_DIR=bin

# Build
build:
	go build -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/open-bbcd

run:
	go run ./cmd/open-bbcd

test:
	go test -v -race ./...

clean:
	rm -rf $(BUILD_DIR)

# Migrations (requires goose: go install github.com/pressly/goose/v3/cmd/goose@latest)
migrate-up:
	goose -dir migrations postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir migrations postgres "$(DATABASE_URL)" down

migrate-status:
	goose -dir migrations postgres "$(DATABASE_URL)" status

migrate-create:
	goose -dir migrations create $(name) sql

.DEFAULT_GOAL := build
