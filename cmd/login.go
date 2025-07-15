package cmd

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Facets-cloud/facets-sdk-go/facets/client/ui_user_controller"
	"github.com/Facets-cloud/fctl/pkg/config"
	"github.com/spf13/cobra"
	"github.com/yarlson/pin"
	"gopkg.in/ini.v1"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate and configure your Facets CLI profile.",
	Long:  `Authenticate with the Facets API and refresh your access token. This command allows you to securely store credentials, manage multiple profiles, and ensure your CLI is ready to interact with Facets services.`,
	Run: func(cmd *cobra.Command, args []string) {
		profile, _ := cmd.Flags().GetString("profile")
		host, _ := cmd.Flags().GetString("host")
		username, _ := cmd.Flags().GetString("username")
		token, _ := cmd.Flags().GetString("token")

		reader := bufio.NewReader(os.Stdin)

		// Profile logic: use 'default' if not provided, but prompt if 'default' exists
		if profile == "" {
			profile = "default"
			home, _ := os.UserHomeDir()
			credsPath := home + "/.facets/credentials"
			creds, err := ini.Load(credsPath)
			if err == nil {
				if creds.Section("default").HasKey("username") {
					fmt.Print("Profile 'default' already exists. Please enter a new profile name: ")
					input, _ := reader.ReadString('\n')
					profile = strings.TrimSpace(input)
					if profile == "" {
						fmt.Println("❌ Profile name cannot be empty.")
						return
					}
				}
			}
		}

		// Prompt for missing host
		if host == "" {
			for {
				fmt.Print("Enter Facets API host (control_plane_url): ")
				input, _ := reader.ReadString('\n')
				host = strings.TrimSpace(input)
				if host == "" {
					fmt.Println("❌ Host cannot be empty.")
					continue
				}
				// If no protocol, prepend https://
				if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
					fmt.Printf("ℹ️  No protocol specified for host. Using https://%s\n", host)
					host = "https://" + host
				}
				parsed, err := url.Parse(host)
				if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
					fmt.Println("❌ Invalid URL. Please enter a valid http(s) URL, e.g. https://facetsdemo.console.facets.cloud")
					host = ""
					continue
				}
				break
			}
		} else {
			// If no protocol, prepend https://
			if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
				fmt.Printf("ℹ️  No protocol specified for host. Using https://%s\n", host)
				host = "https://" + host
			}
			parsed, err := url.Parse(host)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				fmt.Println("❌ Invalid host provided via flag. Please provide a valid http(s) URL.")
				return
			}
		}
		// Prompt for missing username
		if username == "" {
			fmt.Print("Enter Facets username: ")
			input, _ := reader.ReadString('\n')
			username = strings.TrimSpace(input)
			if username == "" {
				fmt.Println("❌ Username cannot be empty.")
				return
			}
		}
		// Prompt for missing token
		if token == "" {
			fmt.Print("Enter Facets API token: ")
			input, _ := reader.ReadString('\n')
			token = strings.TrimSpace(input)
			if token == "" {
				fmt.Println("❌ Token cannot be empty.")
				return
			}
		}

		s := pin.New("🔐 Initializing login...",
			pin.WithSpinnerColor(pin.ColorCyan),
			pin.WithTextColor(pin.ColorYellow),
			pin.WithDoneSymbol('✔'),
			pin.WithDoneSymbolColor(pin.ColorGreen),
			pin.WithPrefix("pin"),
			pin.WithPrefixColor(pin.ColorMagenta),
			pin.WithSeparatorColor(pin.ColorGray),
		)

		cancel := s.Start(context.Background())
		defer cancel()

		s.UpdateMessage("💾 Updating credentials for profile: " + profile)
		updateProfileCredentials(profile, host, username, token)
		s.UpdateMessage("✨ Credentials updated, verifying connection...")

		// Get client, skipping the expiry check for the login command itself
		client, auth, err := config.GetClient(profile, true)
		if err != nil {
			s.Fail(fmt.Sprintf("❌ Login failed: %v", err))
			return
		}

		params := ui_user_controller.NewGetCurrentUserParams()
		_, err = client.UIUserController.GetCurrentUser(params, auth)

		if err != nil {
			s.Fail(fmt.Sprintf("❌ Authentication failed: %v", err))
			return
		}

		// Determine the profile that was actually used to update its expiry
		usedProfile := getProfileName(profile)
		if usedProfile != "" {
			s.UpdateMessage("⏱️ Updating token expiry...")
			updateProfileExpiry(usedProfile)
			s.Stop(fmt.Sprintf("✅ Successfully logged in! Token expiry updated for profile '%s'", usedProfile))
		} else {
			s.Stop("✅ Successfully logged in!")
		}
	},
}

// getProfileName determines the active profile, falling back to "default"
func getProfileName(profileFlag string) string {
	if profileFlag != "" {
		return profileFlag
	}
	return "default"
}

func updateProfileCredentials(profile, host, username, token string) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("❌ Failed to get home directory: %v\n", err)
		return
	}
	credsPath := home + "/.facets/credentials"

	// Ensure the parent directory exists
	if err := os.MkdirAll(filepath.Dir(credsPath), 0700); err != nil {
		fmt.Printf("❌ Failed to create credentials directory: %v\n", err)
		return
	}

	creds, err := ini.Load(credsPath)
	if err != nil {
		creds = ini.Empty()
	}

	creds.Section(profile).Key("control_plane_url").SetValue(host)
	creds.Section(profile).Key("username").SetValue(username)
	creds.Section(profile).Key("token").SetValue(token)

	if err := creds.SaveTo(credsPath); err != nil {
		fmt.Printf("❌ Failed to save credentials: %v\n", err)
	}

	// Ensure the config file exists and set the default profile
	configPath := home + "/.facets/config"
	configIni := ini.Empty()
	if _, err := os.Stat(configPath); err == nil {
		// File exists, try to load it
		loadedIni, err := ini.Load(configPath)
		if err == nil {
			configIni = loadedIni
		}
	}
	configIni.Section("default").Key("profile").SetValue(profile)
	if err := configIni.SaveTo(configPath); err != nil {
		fmt.Printf("❌ Failed to save config file: %v\n", err)
	}
}

func updateProfileExpiry(profile string) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("⚠️ Warning: Failed to get home directory to update expiry: %v\n", err)
		return
	}
	credsPath := home + "/.facets/credentials"
	creds, err := ini.Load(credsPath)
	if err != nil {
		fmt.Printf("⚠️ Warning: Could not load credentials to update expiry: %v\n", err)
		return
	}

	expiry := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	creds.Section(profile).Key("token_expiry").SetValue(expiry)

	if err := creds.SaveTo(credsPath); err != nil {
		fmt.Printf("⚠️ Warning: Failed to save updated token expiry: %v\n", err)
	}
}

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.Flags().StringP("host", "H", "", "Facets API host (control_plane_url)")
	loginCmd.Flags().StringP("username", "u", "", "Facets username")
	loginCmd.Flags().StringP("token", "t", "", "Facets API token")
}
