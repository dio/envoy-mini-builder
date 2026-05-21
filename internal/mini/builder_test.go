package mini

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSplitUserHost(t *testing.T) {
	tests := []struct {
		name     string
		userHost string
		wantUser string
		wantHost string
	}{
		{name: "host only", userHost: "mini.local", wantHost: "mini.local"},
		{name: "user and host", userHost: "dio@mini", wantUser: "dio", wantHost: "mini"},
		{name: "last at wins", userHost: "first@second@mini", wantUser: "first@second", wantHost: "mini"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotUser, gotHost := splitUserHost(tt.userHost)
			if gotUser != tt.wantUser || gotHost != tt.wantHost {
				t.Fatalf("splitUserHost(%q) = (%q, %q), want (%q, %q)", tt.userHost, gotUser, gotHost, tt.wantUser, tt.wantHost)
			}
		})
	}
}

func TestBuilderSSHArgs(t *testing.T) {
	b := NewBuilder(Config{SSHHost: "dio@mini", SSHPort: 2222})
	got := b.sshArgs("echo ok")
	want := []string{"-p", "2222", "-o", "StrictHostKeyChecking=accept-new", "dio@mini", "echo ok"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sshArgs() = %#v, want %#v", got, want)
	}
}

func TestLastLines(t *testing.T) {
	got := lastLines("one\n\n two \nthree\nfour\n", 3)
	want := "two\nthree\nfour"
	if got != want {
		t.Fatalf("lastLines() = %q, want %q", got, want)
	}
}

func TestBuildPrologueQuotesShellValues(t *testing.T) {
	b := NewBuilder(Config{
		EnvoyRepo: "owner/repo?branch=a&b=c",
		CommitSHA: "abc'123",
		PatchURL:  "",
		BazelJobs: "HOST_CPUS",
		BazelArgs: []string{"--verbose_failures", "--define=quoted=a'b", ""},
		BBKey:     "key'with'quotes",
	})

	cmd := exec.Command("bash", "-c", b.buildPrologue()+`printf '<%s>\n' "$ENVOY_REPO" "$COMMIT_SHA" "$PATCH_URL" "$BAZEL_JOBS" "$BUILDBUDDY_API_KEY"
for arg in "${BAZEL_EXTRA_ARGS[@]}"; do
  printf '[%s]\n' "$arg"
done
`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run prologue: %v", err)
	}

	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	want := []string{
		"<owner/repo?branch=a&b=c>",
		"<abc'123>",
		"<>",
		"<HOST_CPUS>",
		"<key'with'quotes>",
		"[--verbose_failures]",
		"[--define=quoted=a'b]",
		"[]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prologue values = %#v, want %#v", got, want)
	}
}

func TestRemoteScriptRunnerWritesExecutesRemovesTempFile(t *testing.T) {
	cmd := exec.Command("bash", "-c", remoteScriptRunnerDarwin)
	cmd.Stdin = strings.NewReader(`printf 'SCRIPT_PATH:%s\n' "$0"
test -f "$0"
`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("remoteScriptRunnerDarwin failed: %v", err)
	}

	path := strings.TrimPrefix(strings.TrimSpace(string(out)), "SCRIPT_PATH:")
	if path == "" || path == strings.TrimSpace(string(out)) {
		t.Fatalf("runner output %q did not include script path", out)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp script still exists or stat failed unexpectedly: %v", err)
	}
}

func TestRemoteScriptRunnerPreservesExitStatus(t *testing.T) {
	cmd := exec.Command("bash", "-c", remoteScriptRunnerDarwin)
	cmd.Stdin = strings.NewReader("exit 7\n")
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runner error = %v, want exit error", err)
	}
	if got := exitErr.ExitCode(); got != 7 {
		t.Fatalf("exit code = %d, want 7", got)
	}
}

func TestRemoteScriptRunnerDoesNotUseZshReadonlyStatus(t *testing.T) {
	if strings.Contains(remoteScriptRunnerDarwin, "\nstatus=$?") || strings.Contains(remoteScriptRunnerDarwin, `"$status"`) {
		t.Fatalf("remoteScriptRunnerDarwin uses zsh read-only status variable: %s", remoteScriptRunnerDarwin)
	}
	if !strings.Contains(remoteScriptRunnerDarwin, "exit_status=$?") || !strings.Contains(remoteScriptRunnerDarwin, `exit "$exit_status"`) {
		t.Fatalf("remoteScriptRunnerDarwin does not preserve exit status via exit_status: %s", remoteScriptRunnerDarwin)
	}
}

func TestBuilderExecScriptExtractsBinaryPath(t *testing.T) {
	dir := t.TempDir()
	writeFakeExecutable(t, dir, "ssh", `#!/bin/sh
for arg in "$@"; do
  printf '[%s]\n' "$arg" >> "$RECORDER/ssh_args"
done
cat > "$RECORDER/stdin"
printf 'normal log line\n'
printf 'BINARY_PATH:/tmp/envoy\n'
`)
	t.Setenv("RECORDER", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	b := NewBuilder(Config{SSHHost: "dio@mini", SSHPort: 2222})
	got, err := b.execScript(context.Background(), remoteScriptRunnerDarwin, "echo build\n")
	if err != nil {
		t.Fatalf("execScript returned error: %v", err)
	}
	if got != "/tmp/envoy" {
		t.Fatalf("execScript path = %q, want /tmp/envoy", got)
	}

	args := readFile(t, filepath.Join(dir, "ssh_args"))
	for _, want := range []string{"[-p]", "[2222]", "[dio@mini]", `bash "$tmp"`} {
		if !strings.Contains(args, want) {
			t.Fatalf("ssh args %q missing %q", args, want)
		}
	}
	stdin := readFile(t, filepath.Join(dir, "stdin"))
	if stdin != "echo build\n" {
		t.Fatalf("ssh stdin = %q, want uploaded script", stdin)
	}
}

func TestBuilderExecScriptMissingSentinel(t *testing.T) {
	dir := t.TempDir()
	writeFakeExecutable(t, dir, "ssh", `#!/bin/sh
cat >/dev/null
printf 'normal log line\n'
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	b := NewBuilder(Config{SSHHost: "mini", SSHPort: 22})
	_, err := b.execScript(context.Background(), remoteScriptRunnerDarwin, "echo build\n")
	if err == nil || !strings.Contains(err.Error(), "BINARY_PATH sentinel") {
		t.Fatalf("execScript error = %v, want missing sentinel error", err)
	}
}

func TestBuilderExecScriptNonZeroIncludesStderrTail(t *testing.T) {
	dir := t.TempDir()
	writeFakeExecutable(t, dir, "ssh", `#!/bin/sh
cat >/dev/null
for line in one two three four five six; do
  printf '%s\n' "$line" >&2
done
exit 42
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	b := NewBuilder(Config{SSHHost: "mini", SSHPort: 22})
	_, err := b.execScript(context.Background(), remoteScriptRunnerDarwin, "echo build\n")
	if err == nil {
		t.Fatal("execScript succeeded, want failure")
	}
	msg := err.Error()
	for _, want := range []string{"remote build failed", "last stderr:", "two", "six"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("execScript error %q missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "one") {
		t.Fatalf("execScript error %q included stderr outside tail", msg)
	}
}

func TestBuilderScpDownload(t *testing.T) {
	dir := t.TempDir()
	writeFakeExecutable(t, dir, "scp", `#!/bin/sh
dest=
for arg in "$@"; do
  printf '[%s]\n' "$arg" >> "$RECORDER/scp_args"
  dest=$arg
done
printf 'binary' > "$dest"
`)
	t.Setenv("RECORDER", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	localPath := filepath.Join(dir, "envoy")
	b := NewBuilder(Config{SSHHost: "dio@mini", SSHPort: 2222})
	if err := b.scpDownload(context.Background(), "/remote/envoy", localPath); err != nil {
		t.Fatalf("scpDownload returned error: %v", err)
	}

	if got := readFile(t, localPath); got != "binary" {
		t.Fatalf("downloaded file = %q, want binary", got)
	}
	args := readFile(t, filepath.Join(dir, "scp_args"))
	for _, want := range []string{"[-P]", "[2222]", "[dio@mini:/remote/envoy]", "[" + localPath + "]"} {
		if !strings.Contains(args, want) {
			t.Fatalf("scp args %q missing %q", args, want)
		}
	}
}

func TestProgressPrinterInteractiveMaintainsBazelLine(t *testing.T) {
	var out strings.Builder
	printer := newProgressPrinter(&out, true)

	first := "Analyzing: target //source/exe:envoy (5 packages loaded, 0 targets configured)"
	second := "Analyzing: target //source/exe:envoy (125 packages loaded, 60 targets configured)"
	printer.printLine(first)
	printer.printLine(second)
	printer.printLine("INFO: done")

	got := out.String()
	if !strings.Contains(got, "\r"+first) || !strings.Contains(got, "\r"+second) {
		t.Fatalf("interactive progress output = %q, want carriage-return progress updates", got)
	}
	if strings.Contains(got, first+"\n"+second) {
		t.Fatalf("interactive progress output = %q, should not print repeated progress as separate lines", got)
	}
	if !strings.HasSuffix(got, "\nINFO: done\n") {
		t.Fatalf("interactive progress output = %q, want normal line after progress", got)
	}
}

func TestProgressPrinterLogModeKeepsLineOrientedOutput(t *testing.T) {
	var out strings.Builder
	printer := newProgressPrinter(&out, false)

	printer.printLine("Analyzing: target //source/exe:envoy (5 packages loaded, 0 targets configured)")
	printer.printLine("Analyzing: target //source/exe:envoy (125 packages loaded, 60 targets configured)")

	got := out.String()
	if strings.Contains(got, "\r") {
		t.Fatalf("log-mode output = %q, should not contain carriage returns", got)
	}
	if strings.Count(got, "\n") != 2 {
		t.Fatalf("log-mode output = %q, want one newline per progress record", got)
	}
}

func TestSplitLinesAndCarriageReturns(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("one\rtwo\r\nthree\n"))
	scanner.Split(splitLinesAndCarriageReturns)

	var got []string
	for scanner.Scan() {
		got = append(got, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	want := []string{"one", "two", "three"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens = %#v, want %#v", got, want)
	}
}

func TestPlatformIsLinux(t *testing.T) {
	if PlatformMacOSArm64.IsLinux() {
		t.Fatal("macos-arm64 reported as Linux")
	}
	if !PlatformLinuxArm64.IsLinux() {
		t.Fatal("linux-arm64 not reported as Linux")
	}
	if !PlatformLinuxAmd64.IsLinux() {
		t.Fatal("linux-amd64 not reported as Linux")
	}
}

func TestFilteredBazelArgs(t *testing.T) {
	tests := []struct {
		name     string
		platform Platform
		args     []string
		want     []string
	}{
		{
			name:     "unscoped args pass through all platforms",
			platform: PlatformMacOSArm64,
			args:     []string{"--verbose_failures", "--define=foo=bar"},
			want:     []string{"--verbose_failures", "--define=foo=bar"},
		},
		{
			name:     "matching platform scoped arg included",
			platform: PlatformLinuxArm64,
			args:     []string{"linux-arm64:--some-flag", "--unscoped"},
			want:     []string{"--some-flag", "--unscoped"},
		},
		{
			name:     "non-matching platform scoped arg excluded",
			platform: PlatformMacOSArm64,
			args:     []string{"linux-arm64:--linux-only", "--common"},
			want:     []string{"--common"},
		},
		{
			name:     "all three platforms mixed",
			platform: PlatformLinuxAmd64,
			args: []string{
				"macos-arm64:--mac-flag",
				"linux-arm64:--arm-flag",
				"linux-amd64:--amd-flag",
				"--always",
			},
			want: []string{"--amd-flag", "--always"},
		},
		{
			name:     "zero platform defaults to macos-arm64",
			platform: "",
			args:     []string{"macos-arm64:--mac-flag", "linux-arm64:--linux-flag"},
			want:     []string{"--mac-flag"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBuilder(Config{Platform: tt.platform, BazelArgs: tt.args})
			got := b.filteredBazelArgs()
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("filteredBazelArgs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestLinuxScriptRunnerContainsOrbRun(t *testing.T) {
	b := NewBuilder(Config{Platform: PlatformLinuxArm64})
	runner := b.linuxScriptRunner()
	for _, want := range []string{
		"orb run -m",
		"linux-arm64",
		`bash -s < "$tmp"`,
		"exit_status=$?",
		`exit "$exit_status"`,
	} {
		if !strings.Contains(runner, want) {
			t.Fatalf("linuxScriptRunner() missing %q", want)
		}
	}
}

func TestDetachedRunnerDetachesFromSSH(t *testing.T) {
	b := NewBuilder(Config{Platform: PlatformMacOSArm64})
	runner := b.detachedRunner("/tmp/jobs/envoy-abc123ef-macos-arm64")
	// nohup must close stdin/stdout/stderr so SSH exits immediately instead
	// of hanging waiting for the background process's file descriptors.
	for _, want := range []string{
		"nohup",
		"</dev/null >/dev/null 2>&1 &",
		"JOB_DIR:",
		"exit_code",
		"binary_path",
	} {
		if !strings.Contains(runner, want) {
			t.Fatalf("detachedRunner() missing %q", want)
		}
	}
}

func writeFakeExecutable(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
