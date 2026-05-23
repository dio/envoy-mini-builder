package mini

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// CleanConfig holds the SSH target for a cache-clean operation.
type CleanConfig struct {
	SSHHost string
	SSHPort int
}

// Cleaner SSHes to the remote host and runs bazel clean --expunge on all
// Envoy builder workspaces under ~/envoy-builder/.
type Cleaner struct {
	cfg CleanConfig
}

// NewCleaner returns a Cleaner for the given config.
func NewCleaner(cfg CleanConfig) *Cleaner {
	return &Cleaner{cfg: cfg}
}

// remoteCleanScript finds all Bazel workspaces under ~/envoy-builder/ and
// runs bazel clean --expunge in each. macOS workspaces run directly; Linux
// workspaces run inside the OrbStack VM via orb run.
const remoteCleanScript = `
set -euo pipefail
PATH="/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:$PATH"
export PATH

shopt -s nullglob
cleaned=0

# ── macOS workspaces ──────────────────────────────────────────────────────────
for src in ~/envoy-builder/*/src; do
  if [[ -f "$src/WORKSPACE" || -f "$src/MODULE.bazel" ]]; then
    echo "→ cleaning (macOS) $src"
    cd "$src"
    bazel clean --expunge 2>&1 || echo "  (bazel not found or failed, skipping)"
    cleaned=$((cleaned + 1))
  fi
done

# ── Linux workspaces (OrbStack VMs) ──────────────────────────────────────────
for machine in linux-arm64 linux-amd64; do
  if ! PATH=/opt/homebrew/bin:$PATH orb list 2>/dev/null | grep -q "^${machine}"; then
    echo "→ skipping $machine (VM not found)"
    continue
  fi
  echo "→ cleaning (${machine} via orb)"
  PATH=/opt/homebrew/bin:$PATH orb run -m "$machine" bash -s << 'ORBSCRIPT'
set -euo pipefail
shopt -s nullglob
for src in ~/envoy-builder/*/src; do
  if [[ -f "$src/WORKSPACE" || -f "$src/MODULE.bazel" ]]; then
    echo "  → $src"
    cd "$src"
    bazel clean --expunge 2>&1 || echo "    (bazel not found or failed, skipping)"
  fi
done
ORBSCRIPT
  cleaned=$((cleaned + 1))
done

if [[ $cleaned -eq 0 ]]; then
  echo "→ no workspaces found"
else
  echo "→ done (cleaned $cleaned target(s))"
fi
`

// Run SSHes to the remote host and cleans all Bazel workspaces.
func (c *Cleaner) Run(ctx context.Context) error {
	args := c.sshArgs(`bash -s`)
	cmd := exec.CommandContext(ctx, "ssh", args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ssh: %w", err)
	}

	go func() {
		defer stdin.Close()
		fmt.Fprint(stdin, remoteCleanScript) //nolint:errcheck
	}()

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("clean failed: %w", err)
	}
	return nil
}

// sshArgs returns the argument list for ssh(1) targeting c.cfg.SSHHost.
func (c *Cleaner) sshArgs(remoteCmd string) []string {
	user, host := splitUserHost(c.cfg.SSHHost)
	target := host
	if user != "" {
		target = user + "@" + host
	}
	return []string{
		"-p", fmt.Sprintf("%d", c.cfg.SSHPort),
		"-o", "StrictHostKeyChecking=accept-new",
		target,
		remoteCmd,
	}
}
