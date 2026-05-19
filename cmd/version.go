package cmd

import (
	"fmt"

	"github.com/dio/envoy-mini-builder/internal/version"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, _ []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "envoy-mini-builder %s (%s) built %s\n",
			version.Version, version.Commit, version.Date)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
	// Also wire --version flag on root for `brew test` compatibility
	rootCmd.Version = version.Version
}
