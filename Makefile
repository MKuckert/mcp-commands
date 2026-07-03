.PHONY: all build test clean lint cross

BINARY_NAME := mcp-commands
VERSION ?= 0.2.0
LDFLAGS := -X main.serverVersion=$(VERSION)

all: test lint build

build:
	@echo "Compiling $(BINARY_NAME) v$(VERSION)..."
	go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY_NAME) .

test:
	@echo "Running tests..."
	go test -v

lint:
	@echo "Running linter..."
	go vet

clean:
	@echo "Cleaning..."
	@rm -rf dist
	go clean

buildall: clean
	@echo "Cross-compiling $(BINARY_NAME) v$(VERSION)..."
	@mkdir -p dist/linux-amd64 dist/linux-arm64 dist/darwin-amd64 dist/darwin-arm64 dist/windows-amd64
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/linux-amd64/$(BINARY_NAME) .
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/linux-arm64/$(BINARY_NAME) .
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/darwin-amd64/$(BINARY_NAME) .
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/darwin-arm64/$(BINARY_NAME) .
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/windows-amd64/$(BINARY_NAME).exe .
