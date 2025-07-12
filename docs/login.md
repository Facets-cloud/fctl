# `fctl login`

Authenticate and configure your Facets CLI profile.

This command authenticates with the Facets API and refreshes your access token. It allows you to securely store credentials, manage multiple profiles, and ensure your CLI is ready to interact with Facets services.

## Usage

```sh
fctl login [flags]
```

## Flags
- `-H, --host string`: Facets API host (control_plane_url)
- `-u, --username string`: Facets username
- `-t, --token string`: Facets API token
- `-p, --profile string`: The profile to use from your credentials file

## Example

```sh
fctl login --host https://api.facets.cloud --username alice --token <your-token> --profile myprofile
``` 