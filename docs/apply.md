# `fctl apply`

Apply a Terraform export to your Facets environment.

This command applies a Terraform configuration exported from Facets to your target environment. It mimics 'terraform apply', supports state file management, selective module targeting, and can upload release metadata to the control plane for audit and tracking.

## Usage

```sh
fctl apply --zip <exported-zip-file> [flags]
```

## Flags
- `-z, --zip string` (required): Path to the exported zip file
- `-t, --target string`: Module target address for selective releases
- `-s, --state string`: Path to the state file
- `    --backend-type string`: Type of backend (e.g., s3, gcs)
- `    --upload-release-metadata`: Upload release metadata to control plane after apply
- `-p, --profile string`: The profile to use from your credentials file

## Example

```sh
fctl apply --zip terraform-export-myenv-1234-20240607-120000.zip --upload-release-metadata
``` 