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
	// SilenceUsage: don't dump usage on every error — only on flag parse errors.
	// SilenceErrors: we print errors ourselves in main.go for uniform formatting.
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true

	// Re-enable usage on flag errors specifically.
	rootCmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, cmd.UsageString())
		os.Exit(2)
		return nil
	})

	rootCmd.SetOut(os.Stdout)
	rootCmd.SetErr(os.Stderr)
}

// fatal prints a red error and exits 1. Shared by subcommands.
// Prefer returning errors from RunE; use fatal only for pre-flight checks
// that cannot return (e.g. inside init callbacks).
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "\033[31m✗\033[0m "+format+"\n", args...)
	os.Exit(1)
}
