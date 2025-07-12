package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show the CLI version, commit, and build date.",
	Long:  `Display the current version of the fctl CLI, including the git commit hash and build date. Useful for debugging and support.`,

	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("fctl version: %s\ncommit: %s\nbuild date: %s\n", Version, Commit, BuildDate)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
