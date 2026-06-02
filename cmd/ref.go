package cmd

import (
	"fmt"
	"os/exec"
	"strings"
)

// resolveRef resolves a branch name, tag, or short SHA to the full 40-char
// commit SHA via the GitHub API. If ref is already a full 40-char hex SHA it
// is returned unchanged without a network call.
func resolveRef(repo, ref string) (string, error) {
	if isFullSHA(ref) {
		return ref, nil
	}
	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/commits/%s", repo, ref),
		"--jq", ".sha",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve ref %q in %s: %w", ref, repo, err)
	}
	full := strings.TrimSpace(string(out))
	if !isFullSHA(full) {
		return "", fmt.Errorf("unexpected SHA from GitHub API: %q", full)
	}
	return full, nil
}

// resolveAndLog resolves ref and prints an info line if it changed.
func resolveAndLog(repo, ref string) (string, error) {
	infof("resolving %s/%s ...", repo, ref)
	sha, err := resolveRef(repo, ref)
	if err != nil {
		return "", fmt.Errorf("resolve ref: %w", err)
	}
	if sha != ref {
		infof("resolved:  %s → %s", ref, sha)
	}
	return sha, nil
}

func isFullSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
