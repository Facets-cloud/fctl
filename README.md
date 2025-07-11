# Facets Terraform Export

A tool for exporting Terraform configurations from Facets.

## Installation

### Using Go

```bash
go install github.com/Facets-cloud/fctl@latest
```

### Using GitHub Releases

Download the latest binary for your platform from the [GitHub Releases](https://github.com/Facets-cloud/fctl/releases) page.

## Development Setup

1. Clone the repository:
   ```bash
   git clone https://github.com/Facets-cloud/fctl.git
cd fctl
   ```

2. Build the binary:
   ```bash
   make build
   ```

3. Run tests:
   ```bash
   make test
   ```

## Available Make Commands

- `make build`: Build the binary
- `make test`: Run tests
- `make clean`: Clean build artifacts
- `make install`: Install locally
- `make dev`: Build and run for development
- `make fmt`: Format code

## Creating a Release

To create a new release:

1. Tag your commit:
   ```bash
   git tag -a v1.0.0 -m "Release v1.0.0"
   ```

2. Push the tag:
   ```bash
   git push origin v1.0.0
   ```

The GitHub Action will automatically build binaries and create a release. 