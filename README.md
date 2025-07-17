# Facets iac-export Controller

Facets iac-export Controller (fctl). A command-line tool to manage infrastructure, environments, deployments, and resources in an air-gapped clouds. It is designed to help users interact with Facets projects and automate workflows around infrastructure as code, primarily using Terraform..

## Usage

```
fctl [command]
```

## Available Commands
- `apply`       Apply a Terraform export to your Facets environment.
- `completion`  Generate the autocompletion script for the specified shell
- `destroy`     Destroy resources for a Terraform export in your Facets environment.
- `export`      Export a Facets environment as a Terraform configuration.
- `help`        Help about any command
- `login`       Authenticate and configure your Facets CLI profile.
- `plan`        Preview changes for a Terraform export in your Facets environment.
- `repackage`   Tweak the exported zip file by copying files from local into specific paths inside the zip.
- `version`     Show the CLI version, commit, and build date.

## Flags
- `--allow-destroy`    Allow resource destroy by setting prevent_destroy = true in all Terraform resources
- `-h, --help`         Help for fctl
- `-p, --profile`      The profile to use from your credentials file

Use `fctl [command] --help` for more information about a command.

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
   ```

2. Build the binary:
   ```bash
   cd fctl
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