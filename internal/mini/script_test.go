package mini

import (
	"strings"
	"testing"
)

func TestRemoteScriptBazelCommandInvariants(t *testing.T) {
	for _, want := range []string{
		"-c opt",
		"--config=macos",
		"--strip=always",
		"--show_progress_rate_limit=15",
		"//source/exe:envoy",
	} {
		if !strings.Contains(remoteScript, want) {
			t.Fatalf("remoteScript missing %q", want)
		}
	}

	for _, forbidden := range []string{"--config=release", "--//:contrib_enabled=false"} {
		if strings.Contains(remoteScript, forbidden) {
			t.Fatalf("remoteScript contains forbidden flag %q", forbidden)
		}
	}
}

func TestRemoteScriptVerifiesDynamicModulesBeforeBinaryPath(t *testing.T) {
	verifyIndex := strings.Index(remoteScript, "verifying dynamic_modules symbols")
	if verifyIndex < 0 {
		t.Fatal("remoteScript missing dynamic module verification")
	}
	binaryIndex := strings.Index(remoteScript, "BINARY_PATH:")
	if binaryIndex < 0 {
		t.Fatal("remoteScript missing BINARY_PATH sentinel")
	}
	if verifyIndex > binaryIndex {
		t.Fatal("dynamic module verification occurs after BINARY_PATH sentinel")
	}
	if !strings.Contains(remoteScript[verifyIndex:binaryIndex], "exit 1") {
		t.Fatal("dynamic module verification does not hard-fail before BINARY_PATH")
	}
}

func TestRemoteScriptCleansBazelrcCache(t *testing.T) {
	for _, want := range []string{
		"rm -f .bazelrc.cache",
		"trap 'rm -f .bazelrc.cache' EXIT",
		"try-import %workspace%/.bazelrc.cache",
	} {
		if !strings.Contains(remoteScript, want) {
			t.Fatalf("remoteScript missing %q", want)
		}
	}
}
