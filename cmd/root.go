package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "envoy-mini-builder",
	Short: "Build Envoy on a remote Mac mini and publish GitHub release assets",
	Long: `envoy-mini-builder SSHes to a Mac mini, runs an Envoy Bazel build,
downloads the binary, and publishes it as a GitHub release asset.

It mirrors the workflow_dispatch inputs of dio/envoy-builder so you can
trigger Mac builds without waiting for GitHub-hosted macOS runners.`,
}

// Execute is the entry point called from main.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	// Silence cobra's default "Error: …" prefix; we print our own.
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true

	rootCmd.SetOut(os.Stdout)
	rootCmd.SetErr(os.Stderr)
}

// fatal prints a red error and exits 1. Shared by subcommands.
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "\033[31m✗\033[0m "+format+"\n", args...)
	os.Exit(1)
}
