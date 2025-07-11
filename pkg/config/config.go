package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Facets-cloud/facets-sdk-go/facets/client"
	"github.com/go-openapi/runtime"
	httptransport "github.com/go-openapi/runtime/client"
	"github.com/go-openapi/strfmt"
	"gopkg.in/ini.v1"
)

// ClientConfig holds the configuration for a Facets client
type ClientConfig struct {
	ControlPlaneURL string
	Username        string
	Token           string
	TokenExpiry     time.Time
}

// GetClientConfig returns the configuration for the specified profile
func GetClientConfig(profileName string) *ClientConfig {
	// Determine profile to use
	if profileName == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		configPath := home + "/.facets/config"
		cfg, err := ini.Load(configPath)
		if err != nil {
			return nil
		}
		profileName = cfg.Section("default").Key("profile").String()
		if profileName == "" {
			return nil
		}
	}

	// Load credentials
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	credsPath := home + "/.facets/credentials"
	creds, err := ini.Load(credsPath)
	if err != nil {
		return nil
	}

	profile, err := creds.GetSection(profileName)
	if err != nil {
		return nil
	}

	host := profile.Key("control_plane_url").String()
	username := profile.Key("username").String()
	token := profile.Key("token").String()
	tokenExpiryStr := profile.Key("token_expiry").String()

	if host == "" || username == "" || token == "" {
		return nil
	}

	var tokenExpiry time.Time
	if tokenExpiryStr != "" {
		tokenExpiry, err = time.Parse(time.RFC3339, tokenExpiryStr)
		if err != nil {
			return nil
		}
	}

	return &ClientConfig{
		ControlPlaneURL: host,
		Username:        username,
		Token:           token,
		TokenExpiry:     tokenExpiry,
	}
}

func GetClient(profileName string, skipExpiryCheck bool) (*client.Facets, runtime.ClientAuthInfoWriter, error) {
	// Determine profile to use
	if profileName == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, fmt.Errorf("could not get user home directory: %v", err)
		}
		configPath := home + "/.facets/config"
		cfg, err := ini.Load(configPath)
		if err != nil {
			return nil, nil, fmt.Errorf("no profile specified and could not read config file at %s", configPath)
		}
		profileName = cfg.Section("default").Key("profile").String()
		if profileName == "" {
			return nil, nil, fmt.Errorf("no profile specified and no default profile set in %s", configPath)
		}
	}

	// Load credentials
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, fmt.Errorf("could not get user home directory: %v", err)
	}
	credsPath := home + "/.facets/credentials"
	creds, err := ini.Load(credsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("could not read credentials file at %s: %v", credsPath, err)
	}

	profile, err := creds.GetSection(profileName)
	if err != nil {
		return nil, nil, fmt.Errorf("profile '%s' not found in %s", profileName, credsPath)
	}

	host := profile.Key("control_plane_url").String()
	username := profile.Key("username").String()
	token := profile.Key("token").String()
	tokenExpiryStr := profile.Key("token_expiry").String()

	if host == "" || username == "" || token == "" {
		return nil, nil, fmt.Errorf("profile '%s' is missing one of control_plane_url, username, or token", profileName)
	}

	// Check token expiry, unless skipped by the caller (e.g., the login command)
	if !skipExpiryCheck && tokenExpiryStr != "" {
		tokenExpiry, err := time.Parse(time.RFC3339, tokenExpiryStr)
		if err != nil {
			return nil, nil, fmt.Errorf("could not parse token_expiry for profile '%s': %v", profileName, err)
		}
		if time.Now().After(tokenExpiry) {
			return nil, nil, fmt.Errorf("token for profile '%s' has expired. Please run 'login' again", profileName)
		}
	}

	// Sanitize the host URL by removing the scheme.
	cleanHost := strings.TrimPrefix(host, "https://")
	cleanHost = strings.TrimPrefix(cleanHost, "http://")

	// Create client and auth
	transport := httptransport.New(cleanHost, "/", []string{"https"})
	transport.Consumers["application/zip"] = runtime.ByteStreamConsumer()
	facetsClient := client.New(transport, strfmt.Default)
	auth := httptransport.BasicAuth(username, token)

	return facetsClient, auth, nil
}
