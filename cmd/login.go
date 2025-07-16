package cmd

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/Facets-cloud/facets-sdk-go/facets/client/ui_user_controller"
	"github.com/Facets-cloud/fctl/pkg/config"
	"github.com/Facets-cloud/fctl/pkg/utils"
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

		// Profile logic: use 'default' if not provided
		if profile == "" {
			profile = "default"
		}

		// Try to load existing credentials for the profile
		home, _ := os.UserHomeDir()
		credsPath := home + "/.facets/credentials"
		creds, err := ini.Load(credsPath)
		if err == nil {
			section, err := creds.GetSection(profile)
			if err == nil {
				existingHost := section.Key("control_plane_url").String()
				existingUsername := section.Key("username").String()
				existingToken := section.Key("token").String()
				if existingHost != "" && existingUsername != "" && existingToken != "" {
					host = existingHost
					username = existingUsername
					token = existingToken
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
		utils.UpdateProfileCredentials(profile, host, username, token)
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
		usedProfile := utils.GetProfileName(profile)
		if usedProfile != "" {
			s.UpdateMessage("⏱️ Updating token expiry...")
			utils.UpdateProfileExpiry(usedProfile)
			s.Stop(fmt.Sprintf("✅ Successfully logged in! Token expiry updated for profile '%s'", usedProfile))
		} else {
			s.Stop("✅ Successfully logged in!")
		}
	},
}

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.Flags().StringP("host", "H", "", "Facets API host (control_plane_url)")
	loginCmd.Flags().StringP("username", "u", "", "Facets username")
	loginCmd.Flags().StringP("token", "t", "", "Facets API token")
}
