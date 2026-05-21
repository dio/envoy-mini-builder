package mini

import (
	"strings"
	"testing"
)

func TestRemoteScriptBazelCommandInvariants(t *testing.T) {
	for _, want := range []string{
		"--compilation_mode=opt",
		"--curses=no",
		"--verbose_failures",
		"--linkopt=-Wl,-framework,SystemConfiguration",
		"--macos_minimum_os=11.0",
		"--host_macos_minimum_os=11.0",
		"--show_progress_rate_limit=15",
		`${BAZEL_EXTRA_ARGS[@]+"${BAZEL_EXTRA_ARGS[@]}"}`,
		"//source/exe:envoy-static",
	} {
		if !strings.Contains(remoteScriptDarwin, want) {
			t.Fatalf("remoteScriptDarwin missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"--config=release",
		"--//:contrib_enabled=false",
		"--config=macos",
		"-c opt",
	} {
		if strings.Contains(remoteScriptDarwin, forbidden) {
			t.Fatalf("remoteScriptDarwin contains forbidden flag %q", forbidden)
		}
	}
}

func TestRemoteScriptBinaryPath(t *testing.T) {
	if !strings.Contains(remoteScriptDarwin, "bazel-bin/source/exe/envoy-static") {
		t.Fatal("remoteScriptDarwin does not reference envoy-static binary path")
	}
	// Post-build strip -x preserves global/exported symbols (N_EXT) including
	// the Mach-O export trie entries needed by dlsym. Plain strip removes
	// non-weak exported symbols from the export trie on MH_EXECUTE.
	if !strings.Contains(remoteScriptDarwin, "strip -x ") {
		t.Fatal("remoteScriptDarwin missing post-build strip -x command")
	}
}

func TestRemoteScriptAppliesExtraBazelArgsToBuild(t *testing.T) {
	buildIndex := strings.Index(remoteScriptDarwin, "  build \\\n")
	if buildIndex < 0 {
		t.Fatal("remoteScriptDarwin missing bazel build command")
	}
	if !strings.Contains(remoteScriptDarwin[buildIndex:], `${BAZEL_EXTRA_ARGS[@]+"${BAZEL_EXTRA_ARGS[@]}"}`) {
		t.Fatal("bazel build does not include BAZEL_EXTRA_ARGS")
	}
}

func TestRemoteScriptDynamicModuleSymbolsInformational(t *testing.T) {
	// Verification is informational — no hard exit before BINARY_PATH.
	binaryIndex := strings.Index(remoteScriptDarwin, "BINARY_PATH:")
	if binaryIndex < 0 {
		t.Fatal("remoteScriptDarwin missing BINARY_PATH sentinel")
	}
	// Any exit 1 before BINARY_PATH should only be for infrastructure failures
	// (binary not found), not symbol counts.
	beforeSentinel := remoteScriptDarwin[:binaryIndex]
	if strings.Contains(beforeSentinel, "envoy_dynamic_module_callback_http_filter") &&
		strings.Contains(beforeSentinel, "exit 1") {
		t.Fatal("remoteScriptDarwin hard-fails on http_filter symbol check — should be informational only")
	}
}

func TestRemoteScriptCleansBazelrcCache(t *testing.T) {
	for _, want := range []string{
		`BAZELRC_CACHE="${WORK_DIR}/.bazelrc.cache"`,
		`rm -f "${BAZELRC_CACHE}"`,
		`trap 'rm -f "${BAZELRC_CACHE}"' EXIT`,
		`--bazelrc=${BAZELRC_CACHE}`,
	} {
		if !strings.Contains(remoteScriptDarwin, want) {
			t.Fatalf("remoteScriptDarwin missing %q", want)
		}
	}
	// Must not touch the workspace .bazelrc — that causes "Modified" version stamp.
	if strings.Contains(remoteScriptDarwin, ">> .bazelrc") || strings.Contains(remoteScriptDarwin, "try-import") {
		t.Fatal("remoteScriptDarwin modifies workspace .bazelrc — will produce Modified build")
	}
}

func TestRemoteScriptLinuxBazelCommandInvariants(t *testing.T) {
	for _, want := range []string{
		"--compilation_mode=opt",
		"--curses=no",
		"--verbose_failures",
		"--show_progress_rate_limit=15",
		`${BAZEL_EXTRA_ARGS[@]+"${BAZEL_EXTRA_ARGS[@]}"}`,
		"//source/exe:envoy-static",
	} {
		if !strings.Contains(remoteScriptLinux, want) {
			t.Fatalf("remoteScriptLinux missing %q", want)
		}
	}
}

func TestRemoteScriptLinuxHasNoMacOSFlags(t *testing.T) {
	for _, forbidden := range []string{
		"--linkopt=-Wl,-framework,SystemConfiguration",
		"--macos_minimum_os",
		"--host_macos_minimum_os",
		"sw_vers",
		"/opt/homebrew",
		"brew install",
	} {
		if strings.Contains(remoteScriptLinux, forbidden) {
			t.Fatalf("remoteScriptLinux contains macOS-specific content %q", forbidden)
		}
	}
}

func TestRemoteScriptLinuxBootstrap(t *testing.T) {
	if !strings.Contains(remoteScriptLinux, "apt-get") {
		t.Fatal("remoteScriptLinux missing apt-get bootstrap")
	}
}

func TestRemoteScriptLinuxBinaryPath(t *testing.T) {
	if !strings.Contains(remoteScriptLinux, "bazel-bin/source/exe/envoy-static") {
		t.Fatal("remoteScriptLinux does not reference envoy-static binary path")
	}
	if !strings.Contains(remoteScriptLinux, "strip -x ") {
		t.Fatal("remoteScriptLinux missing post-build strip -x command")
	}
}

func TestRemoteScriptLinuxCleansBazelrcCache(t *testing.T) {
	for _, want := range []string{
		`BAZELRC_CACHE="${WORK_DIR}/.bazelrc.cache"`,
		`rm -f "${BAZELRC_CACHE}"`,
		`trap 'rm -f "${BAZELRC_CACHE}"' EXIT`,
		`--bazelrc=${BAZELRC_CACHE}`,
	} {
		if !strings.Contains(remoteScriptLinux, want) {
			t.Fatalf("remoteScriptLinux missing %q", want)
		}
	}
}
