package cmd

import (
	"fmt"
	"strings"

	"github.com/dio/envoy-mini-builder/internal/mini"
	"github.com/spf13/cobra"
)

type queryFlags struct {
	envoyRepo string
	commitSHA string
	patchArg  string
	platform  string
	sshHost   string
	sshPort   int
	bazelJobs string
	bbKey     string
	path      string
	noClean   bool
}

var qf queryFlags

var queryCmd = &cobra.Command{
	Use:   "query [pattern]",
	Short: "Search Bazel targets in the Envoy source tree",
	Long: `Run a bazel query on the remote Mac mini and print matching target labels.

The optional PATTERN argument is a glob matched against target names.
Examples: "cluster*"  "*hostname*"  "DynamicModule*"`,
	Args: cobra.MaximumNArgs(1),
	Example: `  # All targets under test/extensions/clusters
  envoy-mini-builder query --sha main --path test/extensions/clusters/...

  # Targets matching "cluster*" under dynamic_modules
  envoy-mini-builder query "cluster*" --sha main \
    --path test/extensions/clusters/dynamic_modules/...

  # With a local patch applied first (BUILD files may add new targets)
  envoy-mini-builder query --sha main --patch file:///tmp/my.patch`,
	RunE: runQuery,
}

func init() {
	f := queryCmd.Flags()
	f.StringVar(&qf.envoyRepo, "repo", "envoyproxy/envoy", "Source repository (owner/repo)")
	f.StringVar(&qf.commitSHA, "sha", "", "Commit SHA, branch, or tag to query (required)")
	f.StringVar(&qf.patchArg, "patch", "", "Patch to apply before querying: https:// URL or file:// local path")
	f.StringVar(&qf.platform, "platform", string(mini.PlatformDarwinArm64), "Target platform: darwin-arm64 | linux-arm64 | linux-amd64")
	f.StringVar(&qf.sshHost, "host", "dio@mini", "SSH host for the Mac mini")
	f.IntVar(&qf.sshPort, "port", 22, "SSH port")
	f.StringVar(&qf.bazelJobs, "jobs", "HOST_CPUS", "Bazel --jobs value")
	f.StringVar(&qf.bbKey, "bb-key", "", "BuildBuddy API key (optional)")
	f.StringVar(&qf.path, "path", "...", "Bazel package path to search (e.g. test/extensions/clusters/...)")
	f.BoolVar(&qf.noClean, "no-clean", false, "Skip git clean -fdx before querying")

	rootCmd.AddCommand(queryCmd)
}

func runQuery(cmd *cobra.Command, args []string) error {
	if qf.commitSHA == "" {
		return fmt.Errorf("--sha is required")
	}

	pattern := ""
	if len(args) == 1 {
		pattern = args[0]
	}

	sha, err := resolveAndLog(qf.envoyRepo, qf.commitSHA)
	if err != nil {
		return err
	}

	patchURL, patchFile, err := preparePatch(cmd.Context(), qf.sshHost, qf.sshPort, qf.patchArg)
	if err != nil {
		return fmt.Errorf("prepare patch: %w", err)
	}

	infof("repo:     %s", qf.envoyRepo)
	if qf.patchArg != "" {
		infof("patch:    %s", qf.patchArg)
	}
	if pattern != "" {
		infof("pattern:  %s", pattern)
	}
	infof("path:     %s", qf.path)

	bld := mini.NewBuilder(mini.Config{
		SSHHost:   qf.sshHost,
		SSHPort:   qf.sshPort,
		EnvoyRepo: qf.envoyRepo,
		CommitSHA: sha,
		PatchURL:  patchURL,
		PatchFile: patchFile,
		BazelJobs: qf.bazelJobs,
		BBKey:     qf.bbKey,
		SkipClean: qf.noClean,
		Platform:  mini.Platform(qf.platform),
	})

	results, err := bld.Query(cmd.Context(), mini.QueryConfig{
		Pattern: pattern,
		Path:    qf.path,
	})
	if err != nil {
		return err
	}

	fmt.Println(strings.Join(results, "\n"))
	infof("%d target(s)", len(results))
	return nil
}
