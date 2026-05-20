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
		if !strings.Contains(remoteScript, want) {
			t.Fatalf("remoteScript missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"--config=release",
		"--//:contrib_enabled=false",
		"--config=macos",
		"-c opt",
	} {
		if strings.Contains(remoteScript, forbidden) {
			t.Fatalf("remoteScript contains forbidden flag %q", forbidden)
		}
	}
}

func TestRemoteScriptBinaryPath(t *testing.T) {
	if !strings.Contains(remoteScript, "bazel-bin/source/exe/envoy-static") {
		t.Fatal("remoteScript does not reference envoy-static binary path")
	}
	// Post-build strip -x preserves global/exported symbols (N_EXT) including
	// the Mach-O export trie entries needed by dlsym. Plain strip removes
	// non-weak exported symbols from the export trie on MH_EXECUTE.
	if !strings.Contains(remoteScript, "strip -x ") {
		t.Fatal("remoteScript missing post-build strip -x command")
	}
}

func TestRemoteScriptAppliesExtraBazelArgsToBuild(t *testing.T) {
	buildIndex := strings.Index(remoteScript, "  build \\\n")
	if buildIndex < 0 {
		t.Fatal("remoteScript missing bazel build command")
	}
	if !strings.Contains(remoteScript[buildIndex:], `${BAZEL_EXTRA_ARGS[@]+"${BAZEL_EXTRA_ARGS[@]}"}`) {
		t.Fatal("bazel build does not include BAZEL_EXTRA_ARGS")
	}
}

func TestRemoteScriptDynamicModuleSymbolsInformational(t *testing.T) {
	// Verification is informational — no hard exit before BINARY_PATH.
	binaryIndex := strings.Index(remoteScript, "BINARY_PATH:")
	if binaryIndex < 0 {
		t.Fatal("remoteScript missing BINARY_PATH sentinel")
	}
	// Any exit 1 before BINARY_PATH should only be for infrastructure failures
	// (binary not found), not symbol counts.
	beforeSentinel := remoteScript[:binaryIndex]
	if strings.Contains(beforeSentinel, "envoy_dynamic_module_callback_http_filter") &&
		strings.Contains(beforeSentinel, "exit 1") {
		t.Fatal("remoteScript hard-fails on http_filter symbol check — should be informational only")
	}
}

func TestRemoteScriptCleansBazelrcCache(t *testing.T) {
	for _, want := range []string{
		`BAZELRC_CACHE="${WORK_DIR}/.bazelrc.cache"`,
		`rm -f "${BAZELRC_CACHE}"`,
		`trap 'rm -f "${BAZELRC_CACHE}"' EXIT`,
		`--bazelrc=${BAZELRC_CACHE}`,
	} {
		if !strings.Contains(remoteScript, want) {
			t.Fatalf("remoteScript missing %q", want)
		}
	}
	// Must not touch the workspace .bazelrc — that causes "Modified" version stamp.
	if strings.Contains(remoteScript, ">> .bazelrc") || strings.Contains(remoteScript, "try-import") {
		t.Fatal("remoteScript modifies workspace .bazelrc — will produce Modified build")
	}
}
