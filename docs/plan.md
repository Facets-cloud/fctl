# `fctl plan`

Preview changes for a Terraform export in your Facets environment.

This command generates and reviews an execution plan for a Terraform export in your Facets environment. It mimics 'terraform plan', allowing you to see what changes will be made before applying them. Supports state file management and selective module targeting.

## Usage

```sh
fctl plan --zip <exported-zip-file> [flags]
```

## Flags
- `-z, --zip string` (required): Path to the exported zip file
- `-t, --target string`: Module target address for selective releases
- `-s, --state string`: Path to the state file
- `    --backend-type string`: Type of backend (e.g., s3, gcs)
- `-p, --profile string`: The profile to use from your credentials file

## Example

```sh
fctl plan --zip terraform-export-myenv-1234-20240607-120000.zip
``` 