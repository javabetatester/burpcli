.PHONY: build test lint

build:
	go build -o bin/burpui ./cmd/burpui

test:
	go vet ./...
	go test -race ./...

lint:
	gofmt -l .
	golangci-lint run
