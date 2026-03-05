.PHONY: fmt test lint build ci

fmt:
	gofmt -w ./cmd ./internal

test:
	go test ./...

lint:
	go vet ./...

build:
	go build ./...

ci: fmt lint test build
