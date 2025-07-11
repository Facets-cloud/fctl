package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "fctl",
	Short: "A CLI for interacting with the Facets API",
	Long:  `fctl is a command-line tool to manage your Facets projects and environments.`,
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
