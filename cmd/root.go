package cmd

import (
	"fmt"
	"os"

	"github.com/Facets-cloud/fctl/pkg/config"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "fctl",
	Short: "A CLI for interacting with the Facets API",
	Long:  `fctl is a command-line tool to manage your Facets projects and environments.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
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
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringP("profile", "p", "", "The profile to use from your credentials file")
}
