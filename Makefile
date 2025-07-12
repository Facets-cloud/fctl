.PHONY: build test clean install

# Default binary name
BINARY=fctl
VERSION=$(shell git describe --tags --always --dirty)

# Build the binary
build:
	go build -o ./bin/$(BINARY)

# Build the binary with version info injected (matches GitHub Actions)
build-versioned:
	VERSION=$(shell git describe --tags --always --dirty) \
	COMMIT=$(shell git rev-parse --short HEAD) \
	DATE=$(shell date -u +%Y-%m-%dT%H:%M:%SZ) \
	go build -ldflags "-X 'github.com/Facets-cloud/fctl/cmd.Version=$$VERSION' -X 'github.com/Facets-cloud/fctl/cmd.Commit=$$COMMIT' -X 'github.com/Facets-cloud/fctl/cmd.BuildDate=$$DATE'" -o ./bin/$(BINARY)

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