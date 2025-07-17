package cmd

import (
	"fmt"
	"os"

	"github.com/Facets-cloud/fctl/pkg/config"
	"github.com/spf13/cobra"
)

var asciiArt = `

███████╗ ██████╗████████╗██╗     
██╔════╝██╔════╝╚══██╔══╝██║     
█████╗  ██║        ██║   ██║     
██╔══╝  ██║        ██║   ██║     
██║     ╚██████╗   ██║   ███████╗
╚═╝      ╚═════╝   ╚═╝   ╚══════╝
`
var description = "Facets iac-export Controller. A command-line tool to manage infrastructure, environments, deployments, and resources in an air-gapped clouds. It is designed to help users interact with Facets projects and automate workflows around infrastructure as code, primarily using Terraform."

var AllowDestroyFlag bool

var rootCmd = &cobra.Command{
	Use:   "fctl",
	Short: "Facets iac-export Controller: Manage cloud infrastructure, environments, and deployments.",
	Long: `Facets iac-export Controller (fctl) is a powerful tool to manage your Facets projects, environments, deployments, and cloud resources from the command line.

Key Features:
- Authenticate and manage user profiles
- Export and apply Terraform configurations
- Plan and preview infrastructure changes
- View version and build metadata
- And more!`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Print ASCII art banner for every command
		fmt.Println(asciiArt)
		fmt.Printf("\033[35m%s\033[0m\n", description)
		fmt.Println()
		if cmd.Use == "login" {
			return nil
		}
		profile, _ := cmd.Flags().GetString("profile")
		_, _, err := config.GetClient(profile, false)
		if err != nil {
			return fmt.Errorf("\n❌ authentication failed: %v\nPlease run 'fctl login' to authenticate", err)
		}
		return nil
	},
}

func Execute() {
	rootCmd.SuggestionsMinimumDistance = 1
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringP("profile", "p", "", "The profile to use from your credentials file")
	rootCmd.PersistentFlags().BoolVar(&AllowDestroyFlag, "allow-destroy", false, "Allow resource destroy by setting prevent_destroy = true in all Terraform resources")
}
