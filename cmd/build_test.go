package cmd

import (
	"strings"
	"testing"
)

func TestReleaseBodyWithPatchURL(t *testing.T) {
	body := releaseBody("envoyproxy/envoy", "abcdef123456", "https://example.test/patch.diff?x=1&y=2")

	for _, want := range []string{
		"| Source | `envoyproxy/envoy` |",
		"| Commit | `abcdef123456` |",
		"| Patch  | https://example.test/patch.diff?x=1&y=2 |",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("releaseBody missing %q in:\n%s", want, body)
		}
	}
}

func TestReleaseBodyPatchFallback(t *testing.T) {
	body := releaseBody("envoyproxy/envoy", "abcdef123456", "")
	if !strings.Contains(body, "| Patch  | — |") {
		t.Fatalf("releaseBody missing patch fallback in:\n%s", body)
	}
}
