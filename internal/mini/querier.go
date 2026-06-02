package mini

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// QueryConfig parameterizes a remote bazel query run.
type QueryConfig struct {
	// Pattern is a glob matched against target names (e.g. "cluster*", "*hostname*").
	// Empty means all targets.
	Pattern string
	// Path is the Bazel package path to search (e.g. "test/extensions/clusters/...").
	// Defaults to "..." (entire workspace).
	Path string
	// TestType filters by test size: "unit" (small/medium), "integration"
	// (large/enormous), or "" (all).
	TestType string
}

// TestRunConfig parameterizes a remote bazel test run.
type TestRunConfig struct {
	Targets    []string // Bazel targets, e.g. ["//test/extensions/clusters/dynamic_modules:cluster_test"]
	TestFilter string   // GTest/bazel --test_filter value; empty means run all
}

// Query runs a bazel query on the remote host and returns the matching target labels.
func (b *Builder) Query(ctx context.Context, qc QueryConfig) ([]string, error) {
	expr := buildQueryExpr(qc.Pattern, qc.Path, false)
	return b.runRemoteQuery(ctx, expr)
}

// TestList runs a bazel query for test targets on the remote host.
func (b *Builder) TestList(ctx context.Context, qc QueryConfig) ([]string, error) {
	expr := buildQueryExpr(qc.Pattern, qc.Path, true)
	if qc.TestType != "" {
		if sizeAttr := testTypeSizeAttr(qc.TestType); sizeAttr != "" {
			expr = fmt.Sprintf("attr(size, %q, %s)", sizeAttr, expr)
		}
	}
	return b.runRemoteQuery(ctx, expr)
}

// Test runs the given test targets on the remote host, streaming output to
// stdout. Returns an error if any test fails.
func (b *Builder) Test(ctx context.Context, tc TestRunConfig) error {
	plat := b.cfg.Platform.resolved()

	prologue := b.buildPrologue() + b.testPrologue(tc)
	runner := remoteScriptRunnerDarwin
	script := prologue + remoteSetupDarwin + remoteTestActionDarwin
	if plat.IsLinux() {
		runner = b.linuxScriptRunner()
		script = prologue + remoteSetupLinux + remoteTestActionLinux
	}

	result, err := b.execScriptCollect(ctx, runner, script, "TEST_RESULT:")
	if err != nil {
		return err
	}
	for _, line := range result {
		if line == "PASS" {
			return nil
		}
		if strings.HasPrefix(line, "FAIL:") {
			return fmt.Errorf("remote tests failed (exit %s)", strings.TrimPrefix(line, "FAIL:"))
		}
	}
	return fmt.Errorf("TEST_RESULT sentinel not emitted by remote script")
}

// ScpUpload uploads a local file to the remote host via scp(1). It is an
// exported helper so cmd packages can stage local patch files before a build
// or test run.
func ScpUpload(ctx context.Context, sshHost string, sshPort int, localPath, remotePath string) error {
	user, host := splitUserHost(sshHost)
	remote := fmt.Sprintf("%s:%s", host, remotePath)
	if user != "" {
		remote = fmt.Sprintf("%s@%s:%s", user, host, remotePath)
	}
	cmd := exec.CommandContext(ctx, "scp",
		"-P", fmt.Sprintf("%d", sshPort),
		"-o", "StrictHostKeyChecking=accept-new",
		localPath,
		remote,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runRemoteQuery executes a bazel query on the remote host and returns the
// collected QUERY_RESULT: lines (with the prefix stripped).
func (b *Builder) runRemoteQuery(ctx context.Context, expr string) ([]string, error) {
	plat := b.cfg.Platform.resolved()

	prologue := b.buildPrologue() + b.queryPrologue(expr)
	runner := remoteScriptRunnerDarwin
	script := prologue + remoteSetupDarwin + remoteQueryActionDarwin
	if plat.IsLinux() {
		runner = b.linuxScriptRunner()
		script = prologue + remoteSetupLinux + remoteQueryActionLinux
	}

	return b.execScriptCollect(ctx, runner, script, "QUERY_RESULT:")
}

// queryPrologue returns a shell snippet exporting QUERY_EXPR.
func (b *Builder) queryPrologue(expr string) string {
	return fmt.Sprintf("QUERY_EXPR=%s\nexport QUERY_EXPR\n", shellQuote(expr))
}

// testPrologue returns a shell snippet exporting TEST_TARGETS array and TEST_FILTER.
func (b *Builder) testPrologue(tc TestRunConfig) string {
	var sb strings.Builder
	sb.WriteString("TEST_TARGETS=(\n")
	for _, t := range tc.Targets {
		fmt.Fprintf(&sb, "  %s\n", shellQuote(t))
	}
	sb.WriteString(")\n")
	fmt.Fprintf(&sb, "TEST_FILTER=%s\n", shellQuote(tc.TestFilter))
	sb.WriteString("export TEST_FILTER\n")
	return sb.String()
}

// buildQueryExpr constructs a bazel query expression from a glob pattern, a
// package path, and a flag indicating whether to restrict to test targets.
func buildQueryExpr(pattern, path string, testsOnly bool) string {
	if path == "" {
		path = "..."
	}
	if !strings.HasPrefix(path, "//") {
		path = "//" + path
	}
	// Ensure the path ends with /... so it searches recursively, unless the
	// caller already specified a specific target (contains ':').
	if !strings.HasSuffix(path, "...") && !strings.Contains(path, ":") {
		path = strings.TrimRight(path, "/") + "/..."
	}

	base := path
	if testsOnly {
		base = fmt.Sprintf("tests(%s)", path)
	}

	if pattern == "" {
		return base
	}
	return fmt.Sprintf("filter(%q, %s)", globToRE2(pattern), base)
}

// globToRE2 converts a simple glob (only * and ? wildcards) to a RE2 pattern
// suitable for bazel's filter() function.
func globToRE2(glob string) string {
	var sb strings.Builder
	for _, ch := range glob {
		switch ch {
		case '*':
			sb.WriteString(".*")
		case '?':
			sb.WriteByte('.')
		case '.', '+', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			sb.WriteByte('\\')
			sb.WriteRune(ch)
		default:
			sb.WriteRune(ch)
		}
	}
	return sb.String()
}

// testTypeSizeAttr returns the Bazel size attribute regex for the given test
// type name. Returns "" for unknown types (caller treats as "all").
func testTypeSizeAttr(testType string) string {
	switch strings.ToLower(testType) {
	case "unit":
		return "small|medium"
	case "integration":
		return "large|enormous"
	}
	return ""
}
