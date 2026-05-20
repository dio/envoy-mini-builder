package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dio/envoy-mini-builder/internal/github"
	"github.com/dio/envoy-mini-builder/internal/mini"
	"github.com/spf13/cobra"
)

// Build flags
type buildFlags struct {
	envoyRepo  string
	commitSHA  string
	patchURL   string
	releaseTag string
	noRelease  bool
	outDir     string
	suffix     string
	noStrip    bool
	sshHost    string
	sshPort    int
	bazelJobs  string
	bazelArgs  []string
	ghRepo     string
	bbKey      string
}

var bf buildFlags

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build Envoy on the Mac mini and publish a release asset",
	Example: `  # Minimal — build main branch, publish to dio/envoy-builder release
  envoy-mini-builder build --sha main

  # Fork, specific SHA, with patch
  envoy-mini-builder build \
    --repo your-org/envoy \
    --sha  a1b2c3d4 \
    --patch https://gist.githubusercontent.com/dio/.../my.patch

  # Build only, no release
  envoy-mini-builder build --sha main --no-release --out ./dist

  # Add repeatable Bazel flags (use = when the value starts with -)
  envoy-mini-builder build --sha main --bazel-arg=--verbose_failures

  # Custom SSH host + BuildBuddy key
  envoy-mini-builder build --sha main --host user@mymac --bb-key <key>`,
	RunE: runBuild,
}

func init() {
	f := buildCmd.Flags()
	f.StringVar(&bf.envoyRepo, "repo", "envoyproxy/envoy", "Source repository (owner/repo); forks work")
	f.StringVar(&bf.commitSHA, "sha", "", "Commit SHA, branch, or tag to build (required)")
	f.StringVar(&bf.patchURL, "patch", "", "Raw URL to a .patch file applied before build")
	f.StringVar(&bf.releaseTag, "tag", "", "Release tag (default: envoy-{sha8}-{date})")
	f.BoolVar(&bf.noRelease, "no-release", false, "Build only — skip release creation and upload")
	f.StringVar(&bf.outDir, "out", "./dist", "Local directory to save the downloaded binary")
	f.StringVar(&bf.suffix, "suffix", "", "Suffix appended to the output binary name (e.g. -patched → envoy-macos-arm64-patched)")
	f.BoolVar(&bf.noStrip, "no-strip", false, "Skip post-build strip (useful for symbol analysis)")
	f.StringVar(&bf.sshHost, "host", "dio@mini", "SSH host for the Mac mini")
	f.IntVar(&bf.sshPort, "port", 22, "SSH port")
	f.StringVar(&bf.bazelJobs, "jobs", "HOST_CPUS", "Bazel --jobs value")
	f.StringArrayVar(&bf.bazelArgs, "bazel-arg", nil, "Additional Bazel argument appended to build and cquery; repeatable")
	f.StringVar(&bf.ghRepo, "gh-repo", "dio/envoy-builder", "GitHub repo for release assets (owner/repo)")
	f.StringVar(&bf.bbKey, "bb-key", "", "BuildBuddy API key for remote cache (overrides BUILDBUDDY_API_KEY env)")
	_ = buildCmd.MarkFlagRequired("sha")

	rootCmd.AddCommand(buildCmd)
}

func runBuild(cmd *cobra.Command, _ []string) error {
	// Resolve BuildBuddy key: flag > env
	bbKey := bf.bbKey
	if bbKey == "" {
		bbKey = os.Getenv("BUILDBUDDY_API_KEY")
	}

	// Derive release tag
	sha := bf.commitSHA
	shortSHA := sha
	if len(sha) > 8 {
		shortSHA = sha[:8]
	}
	tag := bf.releaseTag
	if tag == "" {
		tag = fmt.Sprintf("envoy-%s-%s", shortSHA, time.Now().UTC().Format("20060102"))
	}

	// Validate gh token is available when we need it
	ghToken := os.Getenv("GITHUB_TOKEN")
	if !bf.noRelease && ghToken == "" {
		return fmt.Errorf("GITHUB_TOKEN env var required for release operations (or use --no-release)")
	}

	infof("repo:     %s", bf.envoyRepo)
	infof("sha:      %s", sha)
	if bf.patchURL != "" {
		infof("patch:    %s", bf.patchURL)
	}
	infof("tag:      %s", tag)
	infof("host:     %s (port %d)", bf.sshHost, bf.sshPort)
	if len(bf.bazelArgs) > 0 {
		infof("bazel:    extra args: %s", strings.Join(bf.bazelArgs, " "))
	}
	if bf.noRelease {
		infof("release:  disabled")
	} else {
		infof("release:  %s", bf.ghRepo)
	}

	// ── Draft release ───────────────────────────────────────────────────────
	var releaseID int64
	if !bf.noRelease {
		header("Create draft release")
		gh := github.NewClient(ghToken)
		body := releaseBody(bf.envoyRepo, sha, bf.patchURL)
		id, err := gh.CreateDraftRelease(bf.ghRepo, tag, body)
		if err != nil {
			return fmt.Errorf("create release: %w", err)
		}
		releaseID = id
		okf("Draft release: %s (id=%d)", tag, releaseID)
	}

	// ── Remote build ────────────────────────────────────────────────────────
	header("Remote build on %s", bf.sshHost)
	bld := mini.NewBuilder(mini.Config{
		SSHHost:   bf.sshHost,
		SSHPort:   bf.sshPort,
		EnvoyRepo: bf.envoyRepo,
		CommitSHA: sha,
		PatchURL:  bf.patchURL,
		BazelJobs: bf.bazelJobs,
		BazelArgs: append([]string(nil), bf.bazelArgs...),
		BBKey:     bbKey,
		NoStrip:   bf.noStrip,
	})

	if err := os.MkdirAll(bf.outDir, 0o755); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}

	localPath := fmt.Sprintf("%s/envoy-macos-arm64%s", strings.TrimRight(bf.outDir, "/"), bf.suffix)
	if err := bld.Run(cmd.Context(), localPath); err != nil {
		// If we created a draft release, mark it failed before exiting
		if !bf.noRelease && releaseID != 0 {
			gh := github.NewClient(ghToken)
			_ = gh.MarkReleaseFailed(bf.ghRepo, releaseID, tag)
		}
		return fmt.Errorf("build failed: %w", err)
	}
	okf("Binary: %s", localPath)

	// ── Upload + publish ─────────────────────────────────────────────────────
	if !bf.noRelease {
		header("Publish release")
		gh := github.NewClient(ghToken)
		if err := gh.UploadAsset(bf.ghRepo, releaseID, "envoy-macos-arm64"+bf.suffix, localPath); err != nil {
			return fmt.Errorf("upload asset: %w", err)
		}
		okf("Asset uploaded")

		if err := gh.PublishRelease(bf.ghRepo, releaseID); err != nil {
			return fmt.Errorf("publish release: %w", err)
		}
		okf("Release published: https://github.com/%s/releases/tag/%s", bf.ghRepo, tag)
	}

	header("Done")
	return nil
}

func releaseBody(repo, sha, patchURL string) string {
	patch := "—"
	if patchURL != "" {
		patch = patchURL
	}
	return fmt.Sprintf(`## Envoy build

| Field  | Value |
|--------|-------|
| Source | `+"`%s`"+` |
| Commit | `+"`%s`"+` |
| Target | macos-arm64 (Mac mini) |
| Patch  | %s |
| Built  | %s |`,
		repo, sha, patch, time.Now().UTC().Format(time.RFC3339))
}

func header(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("\n\033[1m── %s ──\033[0m\n", msg)
}

func infof(format string, args ...any) {
	fmt.Printf("\033[36m▶\033[0m "+format+"\n", args...)
}

func okf(format string, args ...any) {
	fmt.Printf("\033[32m✓\033[0m "+format+"\n", args...)
}
