package cmd

import (
	"strings"
	"testing"
	"time"
)

func TestReleaseBodyWithPatchURL(t *testing.T) {
	body := releaseBody("envoyproxy/envoy", "abcdef123456", "https://example.test/patch.diff?x=1&y=2",
		[]string{"macos-arm64", "linux-arm64"})

	for _, want := range []string{
		"| Source    | `envoyproxy/envoy` |",
		"| Commit    | `abcdef123456` |",
		"| Platforms | macos-arm64, linux-arm64 |",
		"| Patch     | https://example.test/patch.diff?x=1&y=2 |",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("releaseBody missing %q in:\n%s", want, body)
		}
	}
	assertBuiltFieldRFC3339(t, body)
}

func TestReleaseBodyPatchFallback(t *testing.T) {
	body := releaseBody("envoyproxy/envoy", "abcdef123456", "", []string{"macos-arm64"})
	if !strings.Contains(body, "| Patch     | — |") {
		t.Fatalf("releaseBody missing patch fallback in:\n%s", body)
	}
	assertBuiltFieldRFC3339(t, body)
}

func assertBuiltFieldRFC3339(t *testing.T, body string) {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "| Built     | ") {
			continue
		}
		value := strings.TrimSuffix(strings.TrimPrefix(line, "| Built     | "), " |")
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			t.Fatalf("built field %q is not RFC3339: %v", value, err)
		}
		return
	}
	t.Fatalf("releaseBody missing built field in:\n%s", body)
}
