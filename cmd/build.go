package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

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
	downloadOnly bool
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

  # Patched build: suffix distinguishes the asset name and lets you use
  # a custom tag; the release body always records the patch URL.
  envoy-mini-builder build --sha main --patch <url> --suffix=-patched \
    --tag envoy-abcdef12-patched

  # Build only, no release
  envoy-mini-builder build --sha main --no-release --out ./dist

  # Skip build — download existing assets for this SHA/tag
  envoy-mini-builder build --sha main --download-only
  envoy-mini-builder build --sha main --all-platforms --download-only

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
	f.StringVar(&bf.releaseTag, "tag", "", "Release tag (default: envoy-{sha8}); override for patch/variant builds, e.g. envoy-abcdef12-patched")
	f.BoolVar(&bf.noRelease, "no-release", false, "Build only — skip release creation and upload")
	f.BoolVar(&bf.downloadOnly, "download-only", false, "Skip build — download existing release assets for the resolved tag")
	f.StringVar(&bf.outDir, "out", "./dist", "Local directory to save the downloaded binary")
	f.StringVar(&bf.suffix, "suffix", "", "Suffix appended to binary and asset names (e.g. -patched → envoy-macos-arm64-patched)")
	f.BoolVar(&bf.noStrip, "no-strip", false, "Skip post-build strip (useful for symbol analysis)")
	f.StringVar(&bf.platform, "platform", string(mini.PlatformMacOSArm64), "Target platform: macos-arm64 | linux-arm64 | linux-amd64")
	f.BoolVar(&bf.allPlatforms, "all-platforms", false, "Build for all supported platforms sequentially under one release")
	f.StringVar(&bf.sshHost, "host", "dio@mini", "SSH host for the Mac mini")
	f.IntVar(&bf.sshPort, "port", 22, "SSH port")
	f.StringVar(&bf.bazelJobs, "jobs", "HOST_CPUS", "Bazel --jobs value")
	f.StringArrayVar(&bf.bazelArgs, "bazel-arg", nil, "Additional Bazel argument appended to build; repeatable; prefix platform: to scope (e.g. linux-arm64:--flag)")
	f.StringVar(&bf.ghRepo, "gh-repo", "dio/envoy-builder", "GitHub repo for release assets (owner/repo)")
	f.StringVar(&bf.bbKey, "bb-key", "", "BuildBuddy API key (all platforms); per-platform env vars: BUILDBUDDY_API_KEY_MACOS_ARM64, BUILDBUDDY_API_KEY_LINUX_ARM64, BUILDBUDDY_API_KEY_LINUX_AMD64; fallback: BUILDBUDDY_API_KEY")
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

	// Derive release tag: envoy-{sha8} by default — one canonical release per
	// Envoy commit SHA. For patch/variant builds use --tag to override (e.g.
	// --tag envoy-abcdef12-patched) and --suffix to distinguish asset names.
	sha := bf.commitSHA
	shortSHA := sha
	if len(sha) > 8 {
		shortSHA = sha[:8]
	}
	tag := bf.releaseTag
	if tag == "" {
		tag = fmt.Sprintf("envoy-%s", shortSHA)
	}

	// ── Download-only shortcut ───────────────────────────────────────────────
	if bf.downloadOnly {
		if err := os.MkdirAll(bf.outDir, 0o755); err != nil {
			return fmt.Errorf("create out dir: %w", err)
		}
		header("Download assets from %s @ %s", bf.ghRepo, tag)
		for _, plat := range platforms {
			platStr := string(plat)
			pattern := fmt.Sprintf("envoy-%s%s", platStr, bf.suffix)
			if err := ghRun("release", "download", tag,
				"--repo", bf.ghRepo,
				"--pattern", pattern,
				"--dir", bf.outDir,
				"--clobber",
			); err != nil {
				return fmt.Errorf("download [%s]: %w", platStr, err)
			}
			okf("Downloaded: %s/%s", strings.TrimRight(bf.outDir, "/"), pattern)
		}
		header("Done")
		return nil
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
	if !bf.noRelease {
		header("Create draft release")
		body := releaseBody(bf.envoyRepo, sha, bf.patchURL, platNames)
		if err := ghCreateDraftRelease(bf.ghRepo, tag, body); err != nil {
			return fmt.Errorf("create release: %w", err)
		}
		okf("Draft release: %s", tag)
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
			if !bf.noRelease {
				_ = ghMarkReleaseFailed(bf.ghRepo, tag)
			}
			return fmt.Errorf("build failed [%s]: %w", platStr, err)
		}
		okf("Binary: %s", localPath)

		if !bf.noRelease {
			// gh release upload uses the file's basename as the asset name,
			// which matches localPath's basename (envoy-<platform><suffix>).
			if err := ghUploadAsset(bf.ghRepo, tag, localPath); err != nil {
				return fmt.Errorf("upload asset [%s]: %w", platStr, err)
			}
			okf("Asset uploaded: envoy-%s%s", platStr, bf.suffix)
		}
	}

	// ── Publish release ──────────────────────────────────────────────────────
	if !bf.noRelease {
		header("Publish release")
		if err := ghPublishRelease(bf.ghRepo, tag); err != nil {
			return fmt.Errorf("publish release: %w", err)
		}
		okf("Release published: https://github.com/%s/releases/tag/%s", bf.ghRepo, tag)
	}

	header("Done")
	return nil
}

// ── gh CLI helpers ────────────────────────────────────────────────────────────

func ghCreateDraftRelease(repo, tag, body string) error {
	return ghRun("release", "create", tag,
		"--repo", repo,
		"--draft",
		"--title", tag,
		"--notes", body,
	)
}

func ghUploadAsset(repo, tag, localPath string) error {
	return ghRun("release", "upload", tag, localPath,
		"--repo", repo,
		"--clobber",
	)
}

func ghPublishRelease(repo, tag string) error {
	return ghRun("release", "edit", tag,
		"--repo", repo,
		"--draft=false",
	)
}

func ghMarkReleaseFailed(repo, tag string) error {
	return ghRun("release", "edit", tag,
		"--repo", repo,
		"--prerelease",
		"--title", tag+" [FAILED]",
	)
}

func ghRun(args ...string) error {
	cmd := exec.Command("gh", args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ── misc helpers ──────────────────────────────────────────────────────────────

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
