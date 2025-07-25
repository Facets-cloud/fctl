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
	Short: "Facets iac-export Controller: Export Facets Environments as Terraform Configurations.",
	Long:  fmt.Sprintf("\033[35m%s\033[0m\n", description),
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(asciiArt)
		fmt.Println()
		cmd.Help()
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
	rootCmd.PersistentFlags().BoolVar(&AllowDestroyFlag, "allow-destroy", false, "Allow resource destroy by setting prevent_destroy = false in all Terraform resources")

	// Move PersistentPreRunE assignment here to avoid initialization cycle
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// Only print banner if not the root command
		if cmd == rootCmd {
			return nil
		}
		fmt.Println(asciiArt)
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
	}
}
