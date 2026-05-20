package mini

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"net"
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

func NewBuilder(cfg Config) *Builder {
	return &Builder{cfg: cfg}
}

// Run connects to the mini, runs the build script, streams logs to stdout,
// and downloads the resulting binary to localPath via SFTP.
//
// The remote script is executed via the system ssh(1) binary so that
// ~/.ssh/config, the ssh-agent, and OpenSSH extensions such as
// publickey-hostbound (OpenSSH 9.x) are honoured. x/crypto/ssh does not
// implement publickey-hostbound and fails to authenticate against OpenSSH 9.x
// servers that have negotiated it.
//
// SFTP (for the binary download) uses x/crypto/ssh over the same transport,
// but by that point we already have an open multiplexed control socket from
// the ssh(1) run, so the connection reuses the existing auth session via
// ControlMaster/ControlPath if configured, or dials a second connection if not.
// On a Mac with a standard ~/.ssh/config the SFTP connection also benefits from
// the Keychain/agent via the same key resolution path.
func (b *Builder) Run(ctx context.Context, localPath string) error {
	prologue := b.buildPrologue()

	remoteBinPath, err := b.execScript(ctx, prologue+remoteScript)
	if err != nil {
		return err
	}

	fmt.Printf("\033[36m▶\033[0m Downloading %s\n", remoteBinPath)

	// SFTP download reuses the same host/port/user. We dial a second SSH
	// connection here using x/crypto/ssh; on macOS this works because the
	// host key and key file are the same. If publickey-hostbound causes a
	// second failure in the future, replace with an `scp` exec.
	client, err := b.dial()
	if err != nil {
		return fmt.Errorf("SFTP dial: %w", err)
	}
	defer client.Close()

	if err := b.sftpDownload(client, remoteBinPath, localPath); err != nil {
		return fmt.Errorf("sftp download: %w", err)
	}
	return nil
}

// execScript runs the build script on the remote host via the system ssh(1)
// binary. This ensures ~/.ssh/config, agent forwarding, and OpenSSH extensions
// are used exactly as they would be from the terminal.
func (b *Builder) execScript(ctx context.Context, script string) (string, error) {
	user, host := splitUserHost(b.cfg.SSHHost)
	target := host
	if user != "" {
		target = user + "@" + host
	}

	args := []string{
		"-p", fmt.Sprintf("%d", b.cfg.SSHPort),
		"-o", "StrictHostKeyChecking=accept-new",
		target,
		"bash -s",
	}

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
	stderrTee := io.MultiWriter(os.Stderr, &stderrBuf)
	cmd.Stderr = stderrTee

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

	if err := cmd.Wait(); err != nil {
		tail := lastLines(stderrBuf.String(), 5)
		if tail != "" {
			return "", fmt.Errorf("remote build failed: %w\nlast stderr:\n%s", err, tail)
		}
		return "", fmt.Errorf("remote build failed: %w", err)
	}
	if remoteBinPath == "" {
		return "", fmt.Errorf("build succeeded but BINARY_PATH sentinel was not emitted")
	}
	return remoteBinPath, nil
}

// dial opens an SSH connection using x/crypto/ssh for SFTP only.
// Uses ssh-agent (preferred) or ~/.ssh/id_ed25519 / id_rsa as fallback.
func (b *Builder) dial() (*ssh.Client, error) {
	user, host := splitUserHost(b.cfg.SSHHost)
	addr := fmt.Sprintf("%s:%d", host, b.cfg.SSHPort)

	var authMethods []ssh.AuthMethod

	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err == nil {
			authMethods = append(authMethods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
		path := fmt.Sprintf("%s/.ssh/%s", os.Getenv("HOME"), name)
		if key, err := loadPrivateKey(path); err == nil {
			authMethods = append(authMethods, ssh.PublicKeys(key))
		}
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no SSH auth methods available (no agent, no key files)")
	}

	cfg := &ssh.ClientConfig{
		User: user,
		Auth: authMethods,
		// sntrup761x25519-sha512 (OpenSSH 9.x default) is not supported by
		// x/crypto/ssh. Enumerate KEX algorithms Go can negotiate.
		Config: ssh.Config{
			KeyExchanges: []string{
				"curve25519-sha256",
				"curve25519-sha256@libssh.org",
				"ecdh-sha2-nistp256",
				"ecdh-sha2-nistp384",
				"ecdh-sha2-nistp521",
			},
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // trusted LAN/Tailscale host
	}
	return ssh.Dial("tcp", addr, cfg)
}

// buildPrologue emits a shell snippet that assigns all user-supplied values
// as quoted variables at the top of the remote script. Single-quote wrapping
// plus escaped inner single-quotes is the simplest portable approach.
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

// sftpDownload copies a remote file to a local path using SFTP over the
// existing SSH connection.
func (b *Builder) sftpDownload(client *ssh.Client, remotePath, localPath string) error {
	sc, err := sftp.NewClient(client)
	if err != nil {
		return err
	}
	defer sc.Close()

	src, err := sc.Open(remotePath)
	if err != nil {
		return fmt.Errorf("open remote %s: %w", remotePath, err)
	}
	defer src.Close()

	dst, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("open local %s: %w", localPath, err)
	}
	defer dst.Close()

	n, err := io.Copy(dst, src)
	if err != nil {
		return err
	}
	fmt.Printf("\033[32m✓\033[0m Downloaded %d bytes → %s\n", n, localPath)
	return nil
}

func splitUserHost(userHost string) (string, string) {
	if i := strings.LastIndex(userHost, "@"); i >= 0 {
		return userHost[:i], userHost[i+1:]
	}
	return "", userHost
}

func loadPrivateKey(path string) (ssh.Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(b)
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
