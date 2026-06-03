package cmd

import (
	"fmt"
	"strings"

	"github.com/dio/envoy-mini-builder/internal/mini"
	"github.com/spf13/cobra"
)

// shared test flags used by both "test ls" and "test run".
type testSharedFlags struct {
	envoyRepo string
	commitSHA string
	patchArg  string
	platform  string
	sshHost   string
	sshPort   int
	bazelJobs string
	bbKey     string
	noClean   bool
}

var testCmd = &cobra.Command{
	Use:   "test",
	Short: "Run or list Envoy Bazel tests on the Mac mini",
}

// ── test ls ──────────────────────────────────────────────────────────────────

type testLsFlags struct {
	testSharedFlags
	path     string
	testType string
}

var tlf testLsFlags

var testLsCmd = &cobra.Command{
	Use:   "ls [pattern]",
	Short: "List available test targets",
	Long: `List Bazel test targets in the Envoy source tree.

The optional PATTERN argument is a glob matched against target names.
Use --type to filter by test size (unit = small/medium, integration = large/enormous).`,
	Args: cobra.MaximumNArgs(1),
	Example: `  # All test targets under clusters
  envoy-mini-builder test ls --sha main --path test/extensions/clusters/...

  # Unit tests matching "cluster*"
  envoy-mini-builder test ls "cluster*" --sha main --type unit \
    --path test/extensions/clusters/...

  # With a local patch (new test targets become visible)
  envoy-mini-builder test ls --sha main --patch file:///tmp/my.patch`,
	RunE: runTestLs,
}

func init() {
	addSharedTestFlags(testLsCmd, &tlf.testSharedFlags)
	f := testLsCmd.Flags()
	f.StringVar(&tlf.path, "path", "...", "Bazel package path to search (e.g. test/extensions/clusters/...)")
	f.StringVar(&tlf.testType, "type", "", "Filter by test type: unit | integration")

	testCmd.AddCommand(testLsCmd)
}

func runTestLs(cmd *cobra.Command, args []string) error {
	if tlf.commitSHA == "" {
		return fmt.Errorf("--sha is required")
	}

	pattern := ""
	if len(args) == 1 {
		pattern = args[0]
	}

	sha, err := resolveAndLog(tlf.envoyRepo, tlf.commitSHA)
	if err != nil {
		return err
	}

	patchURL, patchFile, err := preparePatch(cmd.Context(), tlf.sshHost, tlf.sshPort, tlf.patchArg)
	if err != nil {
		return fmt.Errorf("prepare patch: %w", err)
	}

	infof("repo:     %s", tlf.envoyRepo)
	if tlf.patchArg != "" {
		infof("patch:    %s", tlf.patchArg)
	}
	if pattern != "" {
		infof("pattern:  %s", pattern)
	}
	infof("path:     %s", tlf.path)
	if tlf.testType != "" {
		infof("type:     %s", tlf.testType)
	}

	bld := mini.NewBuilder(mini.Config{
		SSHHost:   tlf.sshHost,
		SSHPort:   tlf.sshPort,
		EnvoyRepo: tlf.envoyRepo,
		CommitSHA: sha,
		PatchURL:  patchURL,
		PatchFile: patchFile,
		BazelJobs: tlf.bazelJobs,
		BBKey:     tlf.bbKey,
		SkipClean: tlf.noClean,
		Platform:  mini.Platform(tlf.platform),
	})

	results, err := bld.TestList(cmd.Context(), mini.QueryConfig{
		Pattern:  pattern,
		Path:     tlf.path,
		TestType: tlf.testType,
	})
	if err != nil {
		return err
	}

	fmt.Println(strings.Join(results, "\n"))
	infof("%d test target(s)", len(results))
	return nil
}

// ── test run ─────────────────────────────────────────────────────────────────

type testRunFlags struct {
	testSharedFlags
	targets    []string
	testFilter string
}

var trf testRunFlags

var testRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run Bazel test targets on the Mac mini",
	Long: `Run one or more Bazel test targets on the remote Mac mini.

Use --filter to run a single GTest case (passed as --test_filter to bazel,
which becomes --gtest_filter inside the test binary).`,
	Example: `  # Run all tests in a package
  envoy-mini-builder test run \
    --sha 0d6e3c60aa55 \
    --target //test/extensions/clusters/dynamic_modules:cluster_test

  # Run a single GTest case via --filter
  envoy-mini-builder test run \
    --sha 0d6e3c60aa55 \
    --target //test/extensions/clusters/dynamic_modules:cluster_test \
    --filter "DynamicModuleClusterTest.AbiCallbacksAddHostsWithHostnames"

  # With a local patch applied first
  envoy-mini-builder test run \
    --sha 0d6e3c60aa55 \
    --patch file:///tmp/my.patch \
    --target //test/extensions/clusters/dynamic_modules:cluster_test \
    --filter "DynamicModuleClusterTest.*Hostname*"`,
	RunE: runTestRun,
}

func init() {
	addSharedTestFlags(testRunCmd, &trf.testSharedFlags)
	f := testRunCmd.Flags()
	f.StringArrayVar(&trf.targets, "target", nil, "Bazel test target; repeatable (required)")
	f.StringVar(&trf.testFilter, "filter", "", "GTest filter passed as --test_filter (e.g. 'TestSuite.TestCase' or '*Pattern*')")

	testCmd.AddCommand(testRunCmd)
	rootCmd.AddCommand(testCmd)
}

func runTestRun(cmd *cobra.Command, args []string) error {
	if trf.commitSHA == "" {
		return fmt.Errorf("--sha is required")
	}
	if len(trf.targets) == 0 {
		return fmt.Errorf("at least one --target is required")
	}

	sha, err := resolveAndLog(trf.envoyRepo, trf.commitSHA)
	if err != nil {
		return err
	}

	patchURL, patchFile, err := preparePatch(cmd.Context(), trf.sshHost, trf.sshPort, trf.patchArg)
	if err != nil {
		return fmt.Errorf("prepare patch: %w", err)
	}

	infof("repo:     %s", trf.envoyRepo)
	if trf.patchArg != "" {
		infof("patch:    %s", trf.patchArg)
	}
	infof("targets:  %s", strings.Join(trf.targets, " "))
	if trf.testFilter != "" {
		infof("filter:   %s", trf.testFilter)
	}

	bld := mini.NewBuilder(mini.Config{
		SSHHost:   trf.sshHost,
		SSHPort:   trf.sshPort,
		EnvoyRepo: trf.envoyRepo,
		CommitSHA: sha,
		PatchURL:  patchURL,
		PatchFile: patchFile,
		BazelJobs: trf.bazelJobs,
		BBKey:     trf.bbKey,
		SkipClean: trf.noClean,
		Platform:  mini.Platform(trf.platform),
	})

	if err := bld.Test(cmd.Context(), mini.TestRunConfig{
		Targets:    trf.targets,
		TestFilter: trf.testFilter,
	}); err != nil {
		return err
	}

	okf("All tests passed")
	return nil
}

// addSharedTestFlags registers the flags shared across "test ls" and "test run".
func addSharedTestFlags(cmd *cobra.Command, sf *testSharedFlags) {
	f := cmd.Flags()
	f.StringVar(&sf.envoyRepo, "repo", "envoyproxy/envoy", "Source repository (owner/repo)")
	f.StringVar(&sf.commitSHA, "sha", "", "Commit SHA, branch, or tag (required)")
	f.StringVar(&sf.patchArg, "patch", "", "Patch to apply: https:// URL or file:// local path")
	f.StringVar(&sf.platform, "platform", string(mini.PlatformDarwinArm64), "Target platform: darwin-arm64 | linux-arm64 | linux-amd64")
	f.StringVar(&sf.sshHost, "host", "dio@mini", "SSH host for the Mac mini")
	f.IntVar(&sf.sshPort, "port", 22, "SSH port")
	f.StringVar(&sf.bazelJobs, "jobs", "HOST_CPUS", "Bazel --jobs value")
	f.StringVar(&sf.bbKey, "bb-key", "", "BuildBuddy API key (optional, speeds up compilation)")
	f.BoolVar(&sf.noClean, "no-clean", false, "Skip git clean -fdx before running (preserves Bazel artifacts for incremental builds)")
}
