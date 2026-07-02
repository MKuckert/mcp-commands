.PHONY: all build test clean lint cross

BINARY_NAME := mcp-commands
VERSION ?= 0.2.0
LDFLAGS := -X main.serverVersion=$(VERSION)
SRC_DIR := src
DIST_DIR := dist

all: test lint build

build:
	@echo "Compiling $(BINARY_NAME) v$(VERSION)..."
	@cd $(SRC_DIR) && go build -ldflags "$(LDFLAGS)" -o ../$(DIST_DIR)/$(BINARY_NAME) .

test:
	@echo "Running tests..."
	@cd $(SRC_DIR) && go test -v ./...

lint:
	@echo "Running linter..."
	@cd $(SRC_DIR) && go vet ./...

clean:
	@echo "Cleaning..."
	@rm -rf $(DIST_DIR)
	@cd $(SRC_DIR) && go clean

buildall: clean
	@echo "Cross-compiling $(BINARY_NAME) v$(VERSION)..."
	@echo "Building for linux/amd64..."
	@mkdir -p $(DIST_DIR)/linux-amd64
	@cd $(SRC_DIR) && GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o ../$(DIST_DIR)/linux-amd64/$(BINARY_NAME) .
	@echo "Building for linux/arm64..."
	@mkdir -p $(DIST_DIR)/linux-arm64
	@cd $(SRC_DIR) && GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o ../$(DIST_DIR)/linux-arm64/$(BINARY_NAME) .
	@echo "Building for darwin/amd64..."
	@mkdir -p $(DIST_DIR)/darwin-amd64
	@cd $(SRC_DIR) && GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o ../$(DIST_DIR)/darwin-amd64/$(BINARY_NAME) .
	@echo "Building for darwin/arm64..."
	@mkdir -p $(DIST_DIR)/darwin-arm64
	@cd $(SRC_DIR) && GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o ../$(DIST_DIR)/darwin-arm64/$(BINARY_NAME) .
	@echo "Building for windows/amd64..."
	@mkdir -p $(DIST_DIR)/windows-amd64
	@cd $(SRC_DIR) && GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o ../$(DIST_DIR)/windows-amd64/$(BINARY_NAME).exe .
