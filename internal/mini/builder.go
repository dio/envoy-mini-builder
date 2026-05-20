package mini

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
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
// and SCPs (via SFTP) the resulting binary to localPath.
func (b *Builder) Run(ctx context.Context, localPath string) error {
	client, err := b.dial()
	if err != nil {
		return fmt.Errorf("SSH dial: %w", err)
	}
	defer client.Close()

	// Pass secrets as env vars in the command prefix -- never written to disk.
	env := b.buildEnv()
	prologue := b.buildPrologue()

	remoteBinPath, err := b.execScript(ctx, client, env, prologue+remoteScript)
	if err != nil {
		return err
	}

	fmt.Printf("\033[36m▶\033[0m Downloading %s\n", remoteBinPath)
	if err := b.sftpDownload(client, remoteBinPath, localPath); err != nil {
		return fmt.Errorf("sftp download: %w", err)
	}
	return nil
}

// dial opens an SSH connection using ssh-agent (preferred) or
// ~/.ssh/id_ed25519 / ~/.ssh/id_rsa as fallback.
func (b *Builder) dial() (*ssh.Client, error) {
	user, host := splitUserHost(b.cfg.SSHHost)
	addr := fmt.Sprintf("%s:%d", host, b.cfg.SSHPort)

	var authMethods []ssh.AuthMethod

	// Try ssh-agent first — it carries whatever keys are loaded, including
	// hardware keys, and avoids passphrase prompts.
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err == nil {
			authMethods = append(authMethods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	// Fallback: known default key files
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
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // known trusted host
	}
	return ssh.Dial("tcp", addr, cfg)
}

func (b *Builder) buildEnv() string {
	// Values are written into the script body via a quoted prologue (see
	// execScript) rather than being passed on the command line. This avoids
	// shell splitting on &, ?, spaces, and other special characters in URLs
	// and tokens. This method now only returns a minimal, safe env prefix
	// that does not carry user-supplied values.
	return "env"
}

// buildPrologue emits a shell snippet that assigns all user-supplied values
// as quoted variables at the top of the remote script. Single-quote wrapping
// plus escaped inner single-quotes is the simplest portable approach.
func (b *Builder) buildPrologue() string {
	vars := map[string]string{
		"ENVOY_REPO":          b.cfg.EnvoyRepo,
		"COMMIT_SHA":          b.cfg.CommitSHA,
		"PATCH_URL":           b.cfg.PatchURL,
		"BAZEL_JOBS":          b.cfg.BazelJobs,
		"BUILDBUDDY_API_KEY":  b.cfg.BBKey,
	}
	var sb strings.Builder
	for k, v := range vars {
		// Single-quote the value; escape any embedded single-quotes as '\''
		safe := strings.ReplaceAll(v, "'", "'\\''")
		fmt.Fprintf(&sb, "%s='%s'\n", k, safe)
	}
	sb.WriteString("export ENVOY_REPO COMMIT_SHA PATCH_URL BAZEL_JOBS BUILDBUDDY_API_KEY\n")
	return sb.String()
}

// execScript runs `env ... bash -s`, pipes the embedded script to stdin,
// streams stdout/stderr to the terminal, and extracts the BINARY_PATH: sentinel.
func (b *Builder) execScript(ctx context.Context, client *ssh.Client, envPrefix, script string) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("new SSH session: %w", err)
	}
	defer sess.Close()

	// Pipe script to remote bash via stdin
	stdin, err := sess.StdinPipe()
	if err != nil {
		return "", err
	}

	stdout, err := sess.StdoutPipe()
	if err != nil {
		return "", err
	}

	// Capture stderr for error context — also tee to os.Stderr so the user
	// sees build output in real time.
	var stderrBuf strings.Builder
	stderrTee := io.MultiWriter(os.Stderr, &stderrBuf)
	sess.Stderr = stderrTee

	cmd := envPrefix + " bash -s"
	if err := sess.Start(cmd); err != nil {
		return "", fmt.Errorf("start remote command: %w", err)
	}

	// Write script to stdin, then close so bash sees EOF
	go func() {
		defer stdin.Close()
		io.WriteString(stdin, script) //nolint:errcheck
	}()

	// Read stdout line by line: print everything, capture sentinel
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

	if err := sess.Wait(); err != nil {
		// Pull the last few lines of stderr for context — the full output was
		// already streamed to the terminal above.
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

// sftpDownload copies a remote file to a local path using SFTP over the
// existing SSH connection. Much faster than spawning a new scp process.
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

// splitUserHost splits "user@host" into ("user", "host").
// If no "@" is present, returns ("", host) and SSH will use the current user.
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
