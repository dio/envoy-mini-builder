package mini

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Config holds everything needed to target a remote Mac mini build.
type Config struct {
	SSHHost   string // e.g. "dio@mini" or "192.168.1.10"
	SSHPort   int
	EnvoyRepo string
	CommitSHA string
	PatchURL  string
	BazelJobs string
	BBKey     string // BuildBuddy API key; empty = local cache only
}

// Builder executes a remote Envoy build and downloads the result.
type Builder struct {
	cfg Config
}

// remoteScriptRunner reads the uploaded script into a file before executing it.
// Running `bash -s` directly is fragile because child processes such as
// Homebrew can consume the remaining script from stdin.
const remoteScriptRunner = `tmp=$(mktemp "${TMPDIR:-/tmp}/envoy-mini-builder.XXXXXX") &&
cat > "$tmp" &&
bash "$tmp"
status=$?
rm -f "$tmp"
exit "$status"`

func NewBuilder(cfg Config) *Builder {
	return &Builder{cfg: cfg}
}

// Run executes the build script on the remote host via system ssh(1) and
// downloads the resulting binary via system scp(1).
//
// Both use the system OpenSSH binaries so that ~/.ssh/config, the ssh-agent,
// and OpenSSH extensions including publickey-hostbound@openssh.com are
// handled correctly. x/crypto/ssh does not implement the hostbound auth
// method; while OpenSSH servers support both plain publickey and the hostbound
// variant (neither is universally mandatory), keeping the entire transport
// inside the system ssh stack avoids the auth gap on both paths.
func (b *Builder) Run(ctx context.Context, localPath string) error {
	prologue := b.buildPrologue()

	remoteBinPath, err := b.execScript(ctx, prologue+remoteScript)
	if err != nil {
		return err
	}

	fmt.Printf("\033[36m▶\033[0m Downloading %s\n", remoteBinPath)
	if err := b.scpDownload(ctx, remoteBinPath, localPath); err != nil {
		return fmt.Errorf("scp download: %w", err)
	}
	return nil
}

// execScript runs the build script on the remote host via system ssh(1).
func (b *Builder) execScript(ctx context.Context, script string) (string, error) {
	args := b.sshArgs(remoteScriptRunner)
	cmd := exec.CommandContext(ctx, "ssh", args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}

	var stderrBuf strings.Builder
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start ssh: %w", err)
	}

	go func() {
		defer stdin.Close()
		io.WriteString(stdin, script) //nolint:errcheck
	}()

	var remoteBinPath string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "BINARY_PATH:") {
			remoteBinPath = strings.TrimPrefix(line, "BINARY_PATH:")
		} else {
			fmt.Println(line)
		}
	}
	scanErr := scanner.Err()

	if err := cmd.Wait(); err != nil {
		tail := lastLines(stderrBuf.String(), 5)
		if tail != "" {
			return "", fmt.Errorf("remote build failed: %w\nlast stderr:\n%s", err, tail)
		}
		return "", fmt.Errorf("remote build failed: %w", err)
	}
	if scanErr != nil {
		return "", fmt.Errorf("read remote stdout: %w", scanErr)
	}
	if remoteBinPath == "" {
		return "", fmt.Errorf("build succeeded but BINARY_PATH sentinel was not emitted")
	}
	return remoteBinPath, nil
}

// scpDownload copies a remote file to localPath using system scp(1).
func (b *Builder) scpDownload(ctx context.Context, remotePath, localPath string) error {
	user, host := splitUserHost(b.cfg.SSHHost)
	remote := fmt.Sprintf("%s:%s", host, remotePath)
	if user != "" {
		remote = fmt.Sprintf("%s@%s:%s", user, host, remotePath)
	}

	args := []string{
		"-P", fmt.Sprintf("%d", b.cfg.SSHPort),
		"-o", "StrictHostKeyChecking=accept-new",
		remote,
		localPath,
	}

	cmd := exec.CommandContext(ctx, "scp", args...)
	cmd.Stdout = os.Stderr // scp progress to stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scp %s: %w", remotePath, err)
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	fmt.Printf("\033[32m✓\033[0m Downloaded %d bytes → %s\n", info.Size(), localPath)
	return nil
}

// sshArgs returns the argument list for ssh(1) targeting b.cfg.SSHHost.
func (b *Builder) sshArgs(remoteCmd string) []string {
	user, host := splitUserHost(b.cfg.SSHHost)
	target := host
	if user != "" {
		target = user + "@" + host
	}
	return []string{
		"-p", fmt.Sprintf("%d", b.cfg.SSHPort),
		"-o", "StrictHostKeyChecking=accept-new",
		target,
		remoteCmd,
	}
}

// buildPrologue emits a shell snippet that assigns all user-supplied values
// as quoted variables. Single-quote wrapping with escaped inner single-quotes
// is the simplest portable approach; values never appear on the command line.
func (b *Builder) buildPrologue() string {
	vars := map[string]string{
		"ENVOY_REPO":         b.cfg.EnvoyRepo,
		"COMMIT_SHA":         b.cfg.CommitSHA,
		"PATCH_URL":          b.cfg.PatchURL,
		"BAZEL_JOBS":         b.cfg.BazelJobs,
		"BUILDBUDDY_API_KEY": b.cfg.BBKey,
	}
	var sb strings.Builder
	for k, v := range vars {
		safe := strings.ReplaceAll(v, "'", "'\\''")
		fmt.Fprintf(&sb, "%s='%s'\n", k, safe)
	}
	sb.WriteString("export ENVOY_REPO COMMIT_SHA PATCH_URL BAZEL_JOBS BUILDBUDDY_API_KEY\n")
	return sb.String()
}

func splitUserHost(userHost string) (string, string) {
	if i := strings.LastIndex(userHost, "@"); i >= 0 {
		return userHost[:i], userHost[i+1:]
	}
	return "", userHost
}

// lastLines returns the last n non-empty lines of s.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			out = append([]string{t}, out...)
		}
	}
	return strings.Join(out, "\n")
}
