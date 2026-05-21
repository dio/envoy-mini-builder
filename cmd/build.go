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
	envoyRepo    string
	commitSHA    string
	patchURL     string
	releaseTag   string
	noRelease    bool
	outDir       string
	suffix       string
	noStrip      bool
	platform     string
	allPlatforms bool
	sshHost      string
	sshPort      int
	bazelJobs    string
	bazelArgs    []string
	ghRepo       string
	bbKey        string
}

var bf buildFlags

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build Envoy on the Mac mini and publish a release asset",
	Example: `  # Minimal — build main branch, publish to dio/envoy-builder release
  envoy-mini-builder build --sha main

  # Build all supported platforms under one release tag
  envoy-mini-builder build --sha main --all-platforms

  # Fork, specific SHA, with patch
  envoy-mini-builder build \
    --repo your-org/envoy \
    --sha  a1b2c3d4 \
    --patch https://gist.githubusercontent.com/dio/.../my.patch

  # Build only, no release
  envoy-mini-builder build --sha main --no-release --out ./dist

  # Add repeatable Bazel flags (use = when the value starts with -)
  envoy-mini-builder build --sha main --bazel-arg=--verbose_failures

  # Scope a Bazel flag to one platform only
  envoy-mini-builder build --sha main \
    --bazel-arg=linux-arm64:--sandbox_base=/tmp/bazel-sandbox

  # BuildBuddy API key — three ways (all may be combined; flag wins):
  #   1. Single key via flag (applies to every platform):
  envoy-mini-builder build --sha main --bb-key <key>
  #   2. Per-platform env vars (different keys per BB org/project):
  export BUILDBUDDY_API_KEY_MACOS_ARM64=<mac-key>
  export BUILDBUDDY_API_KEY_LINUX_ARM64=<linux-arm-key>
  export BUILDBUDDY_API_KEY_LINUX_AMD64=<linux-amd-key>
  envoy-mini-builder build --sha main --all-platforms
  #   3. Generic fallback env var (used when no platform-specific var is set):
  export BUILDBUDDY_API_KEY=<key>
  envoy-mini-builder build --sha main`,
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
	f.StringVar(&bf.platform, "platform", string(mini.PlatformMacOSArm64), "Target platform: macos-arm64 | linux-arm64 | linux-amd64")
	f.BoolVar(&bf.allPlatforms, "all-platforms", false, "Build for all supported platforms sequentially under one release")
	f.StringVar(&bf.sshHost, "host", "dio@mini", "SSH host for the Mac mini")
	f.IntVar(&bf.sshPort, "port", 22, "SSH port")
	f.StringVar(&bf.bazelJobs, "jobs", "HOST_CPUS", "Bazel --jobs value")
	f.StringArrayVar(&bf.bazelArgs, "bazel-arg", nil, "Additional Bazel argument appended to build and cquery; repeatable")
	f.StringVar(&bf.ghRepo, "gh-repo", "dio/envoy-builder", "GitHub repo for release assets (owner/repo)")
	f.StringVar(&bf.bbKey, "bb-key", "", "BuildBuddy API key (applies to all platforms); per-platform env vars take precedence: BUILDBUDDY_API_KEY_MACOS_ARM64, BUILDBUDDY_API_KEY_LINUX_ARM64, BUILDBUDDY_API_KEY_LINUX_AMD64; fallback: BUILDBUDDY_API_KEY")
	_ = buildCmd.MarkFlagRequired("sha")

	rootCmd.AddCommand(buildCmd)
}

// allSupportedPlatforms lists every platform the tool knows how to build.
var allSupportedPlatforms = []mini.Platform{
	mini.PlatformMacOSArm64,
	mini.PlatformLinuxArm64,
	mini.PlatformLinuxAmd64,
}

func runBuild(cmd *cobra.Command, _ []string) error {
	// Determine platform list.
	var platforms []mini.Platform
	if bf.allPlatforms {
		platforms = allSupportedPlatforms
	} else {
		plat := mini.Platform(bf.platform)
		switch plat {
		case mini.PlatformMacOSArm64, mini.PlatformLinuxArm64, mini.PlatformLinuxAmd64:
		default:
			return fmt.Errorf("unknown --platform %q: must be macos-arm64, linux-arm64, or linux-amd64", bf.platform)
		}
		platforms = []mini.Platform{plat}
	}

	// Derive release tag.
	sha := bf.commitSHA
	shortSHA := sha
	if len(sha) > 8 {
		shortSHA = sha[:8]
	}
	tag := bf.releaseTag
	if tag == "" {
		tag = fmt.Sprintf("envoy-%s-%s", shortSHA, time.Now().UTC().Format("20060102"))
	}

	// Validate gh token is available when we need it.
	ghToken := os.Getenv("GITHUB_TOKEN")
	if !bf.noRelease && ghToken == "" {
		return fmt.Errorf("GITHUB_TOKEN env var required for release operations (or use --no-release)")
	}

	platNames := make([]string, len(platforms))
	for i, p := range platforms {
		platNames[i] = string(p)
	}
	infof("platforms: %s", strings.Join(platNames, ", "))
	infof("repo:      %s", bf.envoyRepo)
	infof("sha:       %s", sha)
	if bf.patchURL != "" {
		infof("patch:     %s", bf.patchURL)
	}
	infof("tag:       %s", tag)
	infof("host:      %s (port %d)", bf.sshHost, bf.sshPort)
	if len(bf.bazelArgs) > 0 {
		infof("bazel:     extra args: %s", strings.Join(bf.bazelArgs, " "))
	}
	if bf.noRelease {
		infof("release:   disabled")
	} else {
		infof("release:   %s", bf.ghRepo)
	}

	// ── Draft release (once) ─────────────────────────────────────────────────
	var releaseID int64
	if !bf.noRelease {
		header("Create draft release")
		gh := github.NewClient(ghToken)
		body := releaseBody(bf.envoyRepo, sha, bf.patchURL, platNames)
		id, err := gh.CreateDraftRelease(bf.ghRepo, tag, body)
		if err != nil {
			return fmt.Errorf("create release: %w", err)
		}
		releaseID = id
		okf("Draft release: %s (id=%d)", tag, releaseID)
	}

	if err := os.MkdirAll(bf.outDir, 0o755); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}

	// ── Build each platform ──────────────────────────────────────────────────
	for _, plat := range platforms {
		platStr := string(plat)

		// Resolve BuildBuddy key: flag > platform-specific env > generic env.
		// Platform-specific env vars: BUILDBUDDY_API_KEY_MACOS_ARM64,
		// BUILDBUDDY_API_KEY_LINUX_ARM64, BUILDBUDDY_API_KEY_LINUX_AMD64.
		bbKey := bf.bbKey
		if bbKey == "" {
			envKey := "BUILDBUDDY_API_KEY_" + strings.ToUpper(strings.ReplaceAll(platStr, "-", "_"))
			bbKey = os.Getenv(envKey)
		}
		if bbKey == "" {
			bbKey = os.Getenv("BUILDBUDDY_API_KEY")
		}

		header("Remote build on %s [%s]", bf.sshHost, platStr)
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
			Platform:  plat,
		})

		localPath := fmt.Sprintf("%s/envoy-%s%s", strings.TrimRight(bf.outDir, "/"), platStr, bf.suffix)
		if err := bld.Run(cmd.Context(), localPath); err != nil {
			if !bf.noRelease && releaseID != 0 {
				gh := github.NewClient(ghToken)
				_ = gh.MarkReleaseFailed(bf.ghRepo, releaseID, tag)
			}
			return fmt.Errorf("build failed [%s]: %w", platStr, err)
		}
		okf("Binary: %s", localPath)

		if !bf.noRelease {
			gh := github.NewClient(ghToken)
			assetName := fmt.Sprintf("envoy-%s%s", platStr, bf.suffix)
			if err := gh.UploadAsset(bf.ghRepo, releaseID, assetName, localPath); err != nil {
				return fmt.Errorf("upload asset [%s]: %w", platStr, err)
			}
			okf("Asset uploaded: %s", assetName)
		}
	}

	// ── Publish release ──────────────────────────────────────────────────────
	if !bf.noRelease {
		header("Publish release")
		gh := github.NewClient(ghToken)
		if err := gh.PublishRelease(bf.ghRepo, releaseID); err != nil {
			return fmt.Errorf("publish release: %w", err)
		}
		okf("Release published: https://github.com/%s/releases/tag/%s", bf.ghRepo, tag)
	}

	header("Done")
	return nil
}

func releaseBody(repo, sha, patchURL string, platforms []string) string {
	patch := "—"
	if patchURL != "" {
		patch = patchURL
	}
	return fmt.Sprintf(`## Envoy build

| Field     | Value |
|-----------|-------|
| Source    | `+"`%s`"+` |
| Commit    | `+"`%s`"+` |
| Platforms | %s |
| Patch     | %s |
| Built     | %s |`,
		repo, sha, strings.Join(platforms, ", "), patch, time.Now().UTC().Format(time.RFC3339))
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
