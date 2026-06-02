package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/dio/envoy-mini-builder/internal/mini"
)

// preparePatch resolves the --patch argument (which may be a https:// URL or a
// file:// local path) into the (patchURL, patchFile) pair expected by
// mini.Config.
//
//   - https:// or http:// → patchURL is set; patchFile is empty.
//   - file:// → the local file is uploaded to the remote host via scp; patchURL
//     is cleared and patchFile is set to the remote temp path.
//   - Empty patchArg → both are empty (no patch).
func preparePatch(ctx context.Context, sshHost string, sshPort int, patchArg string) (patchURL, patchFile string, err error) {
	if patchArg == "" {
		return "", "", nil
	}

	switch {
	case strings.HasPrefix(patchArg, "https://") || strings.HasPrefix(patchArg, "http://"):
		return patchArg, "", nil

	case strings.HasPrefix(patchArg, "file://"):
		localPath := strings.TrimPrefix(patchArg, "file://")
		if _, statErr := os.Stat(localPath); statErr != nil {
			return "", "", fmt.Errorf("local patch file not found: %s", localPath)
		}
		remotePath := fmt.Sprintf("/tmp/emb-patch-%d.patch", os.Getpid())
		infof("uploading patch %s → %s:%s", localPath, sshHost, remotePath)
		if uploadErr := mini.ScpUpload(ctx, sshHost, sshPort, localPath, remotePath); uploadErr != nil {
			return "", "", fmt.Errorf("upload patch: %w", uploadErr)
		}
		return "", remotePath, nil

	default:
		return "", "", fmt.Errorf("unsupported patch URL scheme %q: use https:// or file://", patchArg)
	}
}
