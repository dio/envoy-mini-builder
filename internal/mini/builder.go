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

// Download copies a remote file to localPath using system scp(1).
// This is an exported wrapper around scpDownload for use by cmd packages.
func (b *Builder) Download(ctx context.Context, remotePath, localPath string) error {
	return b.scpDownload(ctx, remotePath, localPath)
}

// detachedRunner returns a shell script that:
//  1. Creates the job directory.
//  2. Reads stdin into {jobDir}/build.sh.
//  3. Starts the build in the background via nohup.
//  4. Saves the background PID.
//  5. Prints JOB_DIR with the expanded absolute path.
// detachedBuildCmd returns the shell command that executes the build script
// inside the correct environment (bash directly for macOS, orb run for Linux).
func (b *Builder) detachedBuildCmd(buildSh string) string {
	plat := b.cfg.Platform.resolved()
	if plat.IsLinux() {
		machine := shellQuote(string(plat))
		return `PATH=/opt/homebrew/bin:$PATH orb run -m ` + machine + ` bash -s < ` + jobPath(buildSh)
	}
	return `bash ` + jobPath(buildSh)
}

// jobPath wraps path in double quotes so that $HOME expands on the remote
// shell. Job dir paths are of the form $HOME/envoy-builder/jobs/… which
// contain no characters that need escaping inside double quotes.
func jobPath(path string) string { return `"` + path + `"` }

func (b *Builder) detachedRunner(jobDir string) string {
	buildSh := jobPath(jobDir + "/build.sh")
	buildLog := jobPath(jobDir + "/build.log")
	exitCode := jobPath(jobDir + "/exit_code")
	binaryPath := jobPath(jobDir + "/binary_path")
	pidFile := jobPath(jobDir + "/pid")

	buildCmd := b.detachedBuildCmd(jobDir + "/build.sh")
	inner := buildCmd + " > " + buildLog + " 2>&1; echo $? > " + exitCode + "; grep " +
		shellQuote("^BINARY_PATH:") + " " + buildLog + " 2>/dev/null | tail -1 | sed " +
		shellQuote("s/^BINARY_PATH://") + " > " + binaryPath

	return `mkdir -p ` + jobPath(jobDir) + "\n" +
		`cat > ` + buildSh + "\n" +
		`nohup bash -c ` + shellQuote(inner) + ` </dev/null >/dev/null 2>&1 &` + "\n" +
		`echo $! > ` + pidFile + "\n" +
		`printf 'JOB_DIR:%s\n' ` + jobPath(jobDir)
}

// StartDetached starts a build on the remote host in a detached (background)
// process. It returns quickly; the build continues on the remote. Only macOS
// platform is supported.
//
// jobDir should be a path containing ${HOME} so the remote shell expands it;
// the actual expanded path is returned via the JOB_DIR sentinel in stdout and
// stored in the returned RemoteDir.
func (b *Builder) StartDetached(ctx context.Context, jobDir string) (string, error) {
	plat := b.cfg.Platform.resolved()

	runner := b.detachedRunner(jobDir)
	script := b.buildPrologue() + remoteScriptDarwin
	if plat.IsLinux() {
		script = b.buildPrologue() + remoteScriptLinux
	}

	args := b.sshArgs(runner)
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

	var remoteDir string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	scanner.Split(splitLinesAndCarriageReturns)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "JOB_DIR:") {
			remoteDir = strings.TrimPrefix(line, "JOB_DIR:")
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
			return "", fmt.Errorf("start detached build failed: %w\nlast stderr:\n%s", waitErr, tail)
		}
		return "", fmt.Errorf("start detached build failed: %w", waitErr)
	}
	if scanErr != nil {
		return "", fmt.Errorf("read remote stdout: %w", scanErr)
	}
	if remoteDir == "" {
		return "", fmt.Errorf("detached build started but JOB_DIR sentinel was not emitted")
	}
	return remoteDir, nil
}

// JobStatus SSHes to the remote host and returns the status of a detached job.
// Possible return values: "done:0", "done:N", "running", "unknown".
func (b *Builder) JobStatus(ctx context.Context, remoteDir string) (string, error) {
	exitCodeFile := jobPath(remoteDir + "/exit_code")
	pidFile := jobPath(remoteDir + "/pid")

	remoteCmd := `if [ -f ` + exitCodeFile + ` ]; then
  echo "STATUS:done:$(cat ` + exitCodeFile + `)"
elif kill -0 $(cat ` + pidFile + ` 2>/dev/null) 2>/dev/null; then
  echo "STATUS:running"
else
  echo "STATUS:unknown"
fi`

	out, err := b.sshOutput(ctx, remoteCmd)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "STATUS:") {
			return strings.TrimPrefix(line, "STATUS:"), nil
		}
	}
	return "unknown", nil
}

// ReadBinaryPath SSHes to the remote host and returns the absolute path of
// the built binary as recorded in the job directory.
func (b *Builder) ReadBinaryPath(ctx context.Context, remoteDir string) (string, error) {
	binaryPathFile := jobPath(remoteDir + "/binary_path")
	out, err := b.sshOutput(ctx, "cat "+binaryPathFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CancelJob kills the background build process, shuts down the Bazel server
// (so it releases the lock), and removes the remote job dir.
// It is a best-effort operation: the caller should remove local job state regardless.
func (b *Builder) CancelJob(ctx context.Context, remoteDir string) error {
	pidFile := jobPath(remoteDir + "/pid")
	plat := b.cfg.Platform.resolved()

	// Determine how to reach the Bazel server: Linux builds run inside an
	// OrbStack VM; macOS builds run directly on the mini.
	var bazelShutdown string
	if plat.IsLinux() {
		machine := shellQuote(string(plat))
		bazelShutdown = `PATH=/opt/homebrew/bin:$PATH orb run -m ` + machine + ` bash -c 'pkill -x bazel 2>/dev/null; pkill -x bazelisk 2>/dev/null; true'`
	} else {
		bazelShutdown = `pkill -x bazel 2>/dev/null; pkill -x bazelisk 2>/dev/null; true`
	}

	remoteCmd := `pid=$(cat ` + pidFile + ` 2>/dev/null)
[ -n "$pid" ] && kill -- "-$pid" 2>/dev/null || kill "$pid" 2>/dev/null
` + bazelShutdown + `
rm -rf ` + jobPath(remoteDir)

	_, err := b.sshOutput(ctx, remoteCmd)
	return err
}

// TailLog SSHes to the remote host and tails the build log, connecting
// stdout/stderr to os.Stdout/os.Stderr.
func (b *Builder) TailLog(ctx context.Context, remoteDir string) error {
	buildLog := jobPath(remoteDir + "/build.log")
	args := b.sshArgs("tail -f " + buildLog)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// sshOutput runs a remote command and returns its stdout as a byte slice.
func (b *Builder) sshOutput(ctx context.Context, remoteCmd string) ([]byte, error) {
	args := b.sshArgs(remoteCmd)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ssh %q: %w", remoteCmd, err)
	}
	return out, nil
}

// Platform identifies the target OS/architecture for the build.
type Platform string

const (
	PlatformMacOSArm64 Platform = "macos-arm64"
	PlatformLinuxArm64 Platform = "linux-arm64"
	PlatformLinuxAmd64 Platform = "linux-amd64"
)

// IsLinux reports whether the platform targets a Linux kernel.
func (p Platform) IsLinux() bool {
	return p == PlatformLinuxArm64 || p == PlatformLinuxAmd64
}

// resolved returns the platform, defaulting to PlatformMacOSArm64 if zero.
func (p Platform) resolved() Platform {
	if p == "" {
		return PlatformMacOSArm64
	}
	return p
}

// Config holds everything needed to target a remote Mac mini build.
type Config struct {
	SSHHost   string // e.g. "dio@mini" or "192.168.1.10"
	SSHPort   int
	EnvoyRepo string
	CommitSHA string
	PatchURL  string
	BazelJobs string
	BazelArgs []string
	BBKey     string   // BuildBuddy API key for current platform; empty = local cache only
	NoStrip   bool     // skip post-build strip (useful for symbol analysis)
	Platform  Platform // target platform; defaults to PlatformMacOSArm64 if zero
}

// Builder executes a remote Envoy build and downloads the result.
type Builder struct {
	cfg Config
}

// remoteScriptRunnerDarwin reads the uploaded script into a temp file and
// executes it directly with bash. Running `bash -s` directly is fragile
// because child processes such as Homebrew can consume stdin.
const remoteScriptRunnerDarwin = `tmp=$(mktemp "${TMPDIR:-/tmp}/envoy-mini-builder.XXXXXX") &&
cat > "$tmp" &&
bash "$tmp"
exit_status=$?
rm -f "$tmp"
exit "$exit_status"`

// linuxScriptRunner returns the runner that saves the script to a temp file on
// the Mac mini and pipes it into the target OrbStack Linux machine via orb run.
func (b *Builder) linuxScriptRunner() string {
	machine := string(b.cfg.Platform.resolved())
	return `tmp=$(mktemp "${TMPDIR:-/tmp}/envoy-mini-builder.XXXXXX") &&` + "\n" +
		`cat > "$tmp" &&` + "\n" +
		`PATH=/opt/homebrew/bin:$PATH orb run -m ` + shellQuote(machine) + ` bash -s < "$tmp"` + "\n" +
		`exit_status=$?` + "\n" +
		`rm -f "$tmp"` + "\n" +
		`exit "$exit_status"`
}

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
	plat := b.cfg.Platform.resolved()

	runner := remoteScriptRunnerDarwin
	script := remoteScriptDarwin
	if plat.IsLinux() {
		runner = b.linuxScriptRunner()
		script = remoteScriptLinux
	}

	remoteBinPath, err := b.execScript(ctx, runner, b.buildPrologue()+script)
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
func (b *Builder) execScript(ctx context.Context, runner, script string) (string, error) {
	args := b.sshArgs(runner)
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
	for _, arg := range b.filteredBazelArgs() {
		fmt.Fprintf(&sb, "  %s\n", shellQuote(arg))
	}
	sb.WriteString(")\n")
	sb.WriteString("export ENVOY_REPO COMMIT_SHA PATCH_URL BAZEL_JOBS BUILDBUDDY_API_KEY\n")
	return sb.String()
}

// filteredBazelArgs returns the BazelArgs that apply to the current platform.
// An arg prefixed with "<platform>:" is scoped to that platform only; an arg
// with no recognized platform prefix applies to all platforms.
func (b *Builder) filteredBazelArgs() []string {
	plat := b.cfg.Platform.resolved()
	var out []string
	for _, arg := range b.cfg.BazelArgs {
		if i := strings.Index(arg, ":"); i >= 0 {
			switch Platform(arg[:i]) {
			case PlatformMacOSArm64, PlatformLinuxArm64, PlatformLinuxAmd64:
				if Platform(arg[:i]) == plat {
					out = append(out, arg[i+1:])
				}
				continue
			}
		}
		out = append(out, arg)
	}
	return out
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
