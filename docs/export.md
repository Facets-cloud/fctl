# `fctl export`

Export a Facets environment as a Terraform configuration.

This command exports your Facets project environment as a Terraform configuration zip file. This enables you to manage infrastructure as code, perform offline planning, and apply changes in a controlled manner.

## Usage

```sh
fctl export --environment <environment-id> [flags]
```

## Flags
- `-e, --environment string` (required): The environment to export
- `-p, --profile string`: The profile to use from your credentials file

## Example

```sh
fctl export --environment my-env-id
``` 