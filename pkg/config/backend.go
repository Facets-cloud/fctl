package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BackendConfig represents the configuration for a Terraform backend
type BackendConfig struct {
	Type       string
	ConfigVars map[string]string
}

// S3BackendVars contains required variables for S3 backend
var S3BackendVars = []string{
	"bucket",
	"key",
	"region",
	"access_key",
	"secret_key",
	"dynamodb_table", // optional
	"endpoint",      // optional
	"session_token", // optional
}

// GCSBackendVars contains required variables for GCS backend
var GCSBackendVars = []string{
	"bucket",
	"prefix",
	"credentials",
}

// NewBackendConfig creates a new backend configuration
func NewBackendConfig(backendType string) (*BackendConfig, error) {
	backendType = strings.ToLower(backendType)
	if backendType == "" {
		return nil, nil // Local backend
	}

	config := &BackendConfig{
		Type:       backendType,
		ConfigVars: make(map[string]string),
	}

	var requiredVars []string
	switch backendType {
	case "s3":
		requiredVars = S3BackendVars
	case "gcs":
		requiredVars = GCSBackendVars
	default:
		return nil, fmt.Errorf("unsupported backend type: %s", backendType)
	}

	// Load configuration from environment variables
	for _, v := range requiredVars {
		envVar := fmt.Sprintf("TF_BACKEND_%s_%s", strings.ToUpper(backendType), strings.ToUpper(v))
		if val := os.Getenv(envVar); val != "" {
			config.ConfigVars[v] = val
		}
	}

	return config, nil
}

// GetTerraformConfig returns the backend configuration in Terraform format
func (c *BackendConfig) GetTerraformConfig() map[string]interface{} {
	if c == nil {
		return nil
	}

	config := make(map[string]interface{})
	for k, v := range c.ConfigVars {
		config[k] = v
	}

	return config
}

// GetTerraformConfig returns the backend configuration in Terraform format
func (c *BackendConfig) GetTerraformConfigPairs() []string {
	if c == nil {
		return nil
	}

	var pairs []string
	for k, v := range c.ConfigVars {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, v))
	}

	return pairs
}

// Validate checks if all required variables are set
func (c *BackendConfig) Validate() error {
	if c == nil {
		return nil // Local backend is always valid
	}

	var requiredVars []string
	switch c.Type {
	case "s3":
		requiredVars = []string{"bucket", "key", "region"}
	case "gcs":
		requiredVars = []string{"bucket", "prefix"}
	}

	var missingVars []string
	for _, v := range requiredVars {
		if _, ok := c.ConfigVars[v]; !ok {
			missingVars = append(missingVars, v)
		}
	}

	if len(missingVars) > 0 {
		return fmt.Errorf("missing required backend variables: %s", strings.Join(missingVars, ", "))
	}

	return nil
}

// WriteBackendTFJSON writes a backend.tf.json file in the given directory for this backend config.
func (c *BackendConfig) WriteBackendTFJSON(dir string) error {
	if c == nil {
		return nil // No backend config to write
	}

	backendObj := map[string]interface{}{
		"terraform": map[string]interface{}{
			"backend": map[string]interface{}{
				c.Type: c.GetTerraformConfig(),
			},
		},
	}

	jsonBytes, err := json.MarshalIndent(backendObj, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal backend.tf.json: %w", err)
	}

	filePath := filepath.Join(dir, "backend.tf.json")
	if err := os.WriteFile(filePath, jsonBytes, 0644); err != nil {
		return fmt.Errorf("failed to write backend.tf.json: %w", err)
	}
	return nil
}
