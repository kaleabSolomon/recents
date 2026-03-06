.PHONY: fmt test lint build ci perf

fmt:
	gofmt -w ./cmd ./internal

test:
	go test ./...

lint:
	go vet ./...

build:
	go build ./...

ci: fmt lint test build

perf:
	go test -bench . -benchmem ./...
