package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Facets-cloud/facets-sdk-go/facets/client/ui_user_controller"
	"github.com/Facets-cloud/fctl/pkg/config"
	"github.com/spf13/cobra"
	"github.com/yarlson/pin"
	"gopkg.in/ini.v1"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with the Facets API and refresh token expiry",
	Run: func(cmd *cobra.Command, args []string) {
		profile, _ := cmd.Flags().GetString("profile")
		host, _ := cmd.Flags().GetString("host")
		username, _ := cmd.Flags().GetString("username")
		token, _ := cmd.Flags().GetString("token")

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

		// If flags for credentials are provided, use them to update the profile
		if host != "" && username != "" && token != "" {
			if profile == "" {
				s.Fail("❌ Please specify a --profile <n> to save these credentials.")
				return
			}
			s.UpdateMessage("💾 Updating credentials for profile: " + profile)
			updateProfileCredentials(profile, host, username, token)
			s.UpdateMessage("✨ Credentials updated, verifying connection...")
		} else {
			s.UpdateMessage("🔑 Verifying existing credentials...")
		}

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
