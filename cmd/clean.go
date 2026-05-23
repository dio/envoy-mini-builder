package cmd

import (
	"github.com/dio/envoy-mini-builder/internal/mini"
	"github.com/spf13/cobra"
)

type cleanFlags struct {
	sshHost string
	sshPort int
}

var clnf cleanFlags

var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Clean Bazel cache on the remote host",
	Long: `SSHes to the remote host and runs bazel clean --expunge on all
Envoy builder workspaces under ~/envoy-builder/. Linux workspaces
are cleaned inside their OrbStack VMs.`,
	RunE: runClean,
}

func init() {
	f := cleanCmd.Flags()
	f.StringVar(&clnf.sshHost, "host", "dio@mini", "SSH host")
	f.IntVar(&clnf.sshPort, "port", 22, "SSH port")
	rootCmd.AddCommand(cleanCmd)
}

func runClean(cmd *cobra.Command, _ []string) error {
	header("Clean Bazel cache on %s", clnf.sshHost)
	c := mini.NewCleaner(mini.CleanConfig{
		SSHHost: clnf.sshHost,
		SSHPort: clnf.sshPort,
	})
	if err := c.Run(cmd.Context()); err != nil {
		return err
	}
	okf("Done")
	return nil
}
