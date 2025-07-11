.PHONY: build test clean install

# Default binary name
BINARY=fctl
VERSION=$(shell git describe --tags --always --dirty)

# Build the binary
build:
	go build -o ./bin/$(BINARY)

# Run tests
test:
	go test ./...

# Clean build artifacts
clean:
	rm -f ./bin/$(BINARY)
	rm -f $(BINARY)-*

# Install locally
install:
	go install

# Run local development build
dev: build
	./$(BINARY)

# Format code
fmt:
	go fmt ./... 