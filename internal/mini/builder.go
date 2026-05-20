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
	BazelArgs []string
	BBKey     string // BuildBuddy API key; empty = local cache only
	NoStrip   bool   // skip post-build strip (useful for symbol analysis)
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
exit_status=$?
rm -f "$tmp"
exit "$exit_status"`

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
	stderrWriter := newProgressWriter(os.Stderr, &stderrBuf)
	cmd.Stderr = stderrWriter

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start ssh: %w", err)
	}

	go func() {
		defer stdin.Close()
		io.WriteString(stdin, script) //nolint:errcheck
	}()

	var remoteBinPath string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	scanner.Split(splitLinesAndCarriageReturns)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "BINARY_PATH:") {
			remoteBinPath = strings.TrimPrefix(line, "BINARY_PATH:")
		} else {
			fmt.Println(line)
		}
	}
	scanErr := scanner.Err()

	waitErr := cmd.Wait()
	stderrWriter.finish()
	if waitErr != nil {
		tail := lastLines(stderrBuf.String(), 5)
		if tail != "" {
			return "", fmt.Errorf("remote build failed: %w\nlast stderr:\n%s", waitErr, tail)
		}
		return "", fmt.Errorf("remote build failed: %w", waitErr)
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

// buildPrologue emits a shell snippet that assigns all user-supplied values as
// quoted variables and arrays. Single-quote wrapping with escaped inner
// single-quotes is the simplest portable approach; values never appear on the
// command line.
func (b *Builder) buildPrologue() string {
	skipStrip := ""
	if b.cfg.NoStrip {
		skipStrip = "1"
	}
	vars := map[string]string{
		"ENVOY_REPO":         b.cfg.EnvoyRepo,
		"COMMIT_SHA":         b.cfg.CommitSHA,
		"PATCH_URL":          b.cfg.PatchURL,
		"BAZEL_JOBS":         b.cfg.BazelJobs,
		"BUILDBUDDY_API_KEY": b.cfg.BBKey,
		"SKIP_STRIP":         skipStrip,
	}
	var sb strings.Builder
	for k, v := range vars {
		fmt.Fprintf(&sb, "%s=%s\n", k, shellQuote(v))
	}
	sb.WriteString("BAZEL_EXTRA_ARGS=(\n")
	for _, arg := range b.cfg.BazelArgs {
		fmt.Fprintf(&sb, "  %s\n", shellQuote(arg))
	}
	sb.WriteString(")\n")
	sb.WriteString("export ENVOY_REPO COMMIT_SHA PATCH_URL BAZEL_JOBS BUILDBUDDY_API_KEY\n")
	return sb.String()
}

func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "'\\''") + "'"
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

type progressPrinter struct {
	w           io.Writer
	interactive bool
	active      bool
	width       int
}

func newProgressPrinter(w io.Writer, interactive bool) *progressPrinter {
	return &progressPrinter{w: w, interactive: interactive}
}

func (p *progressPrinter) printLine(line string) {
	if p.interactive && isBazelProgressLine(line) {
		padding := ""
		if p.width > len(line) {
			padding = strings.Repeat(" ", p.width-len(line))
		}
		fmt.Fprintf(p.w, "\r%s%s", line, padding)
		p.active = true
		p.width = len(line)
		return
	}

	p.finish()
	fmt.Fprintln(p.w, line)
}

func (p *progressPrinter) finish() {
	if !p.active {
		return
	}
	fmt.Fprintln(p.w)
	p.active = false
	p.width = 0
}

func isBazelProgressLine(line string) bool {
	for _, prefix := range []string{
		"Loading: ",
		"Loading package: ",
		"Loading configured target: ",
		"Analyzing: ",
		"Checking cached actions: ",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

type progressWriter struct {
	printer *progressPrinter
	capture *strings.Builder
	pending strings.Builder
	skipLF  bool
}

func newProgressWriter(w io.Writer, capture *strings.Builder) *progressWriter {
	return &progressWriter{
		printer: newProgressPrinter(w, useInteractiveProgress(w)),
		capture: capture,
	}
}

func (w *progressWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if w.skipLF {
			w.skipLF = false
			if b == '\n' {
				continue
			}
		}
		switch b {
		case '\n':
			w.flushLine()
		case '\r':
			w.flushLine()
			w.skipLF = true
		default:
			w.pending.WriteByte(b)
		}
	}
	return len(p), nil
}

func (w *progressWriter) finish() {
	if w.pending.Len() > 0 {
		w.flushLine()
	}
	w.printer.finish()
}

func (w *progressWriter) flushLine() {
	line := w.pending.String()
	w.pending.Reset()
	w.capture.WriteString(line)
	w.capture.WriteByte('\n')
	w.printer.printLine(line)
}

func splitLinesAndCarriageReturns(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		switch b {
		case '\n':
			return i + 1, data[:i], nil
		case '\r':
			advance := i + 1
			if advance < len(data) && data[advance] == '\n' {
				advance++
			}
			return advance, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func useInteractiveProgress(w io.Writer) bool {
	return isTerminal(w) && !isCI()
}

func isCI() bool {
	return os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != ""
}
