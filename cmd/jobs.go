package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dio/envoy-mini-builder/internal/mini"
	"github.com/spf13/cobra"
)

// ── jobs ──────────────────────────────────────────────────────────────────────

var jobsCmd = &cobra.Command{
	Use:   "jobs",
	Short: "List detached builds and their current status",
	RunE:  runJobs,
}

func init() {
	rootCmd.AddCommand(jobsCmd)
}

func runJobs(cmd *cobra.Command, _ []string) error {
	jobs, err := mini.LoadJobs()
	if err != nil {
		return fmt.Errorf("load jobs: %w", err)
	}
	if len(jobs) == 0 {
		fmt.Println("No detached jobs found.")
		return nil
	}

	// Group by tag, preserving first-seen order.
	var tagOrder []string
	byTag := map[string][]mini.Job{}
	for _, j := range jobs {
		if _, seen := byTag[j.Tag]; !seen {
			tagOrder = append(tagOrder, j.Tag)
		}
		byTag[j.Tag] = append(byTag[j.Tag], j)
	}

	fmt.Printf("%-22s %-14s %-12s %s\n", "TAG", "PLATFORM", "STATUS", "STARTED")
	for i, tag := range tagOrder {
		if i > 0 {
			fmt.Println()
		}
		for k, j := range byTag[tag] {
			b := mini.NewBuilder(mini.Config{SSHHost: j.SSHHost, SSHPort: j.SSHPort})
			status, err := b.JobStatus(cmd.Context(), j.RemoteDir)
			if err != nil {
				status = "error"
			}
			display := formatStatus(status)
			started := j.StartedAt.Local().Format("2006-01-02 15:04")
			tagCol := ""
			if k == 0 {
				tagCol = tag
			}
			fmt.Printf("%-22s %-14s %-12s %s\n", tagCol, j.Platform, display, started)
		}
	}
	return nil
}

func formatStatus(status string) string {
	switch {
	case status == "done:0":
		return "done \u2713"
	case strings.HasPrefix(status, "done:"):
		return "failed \u2717"
	case status == "running":
		return "running"
	default:
		return "unknown"
	}
}

// ── logs ──────────────────────────────────────────────────────────────────────

var (
	logsCmd      *cobra.Command
	logsPlatform string
)

func init() {
	logsCmd = &cobra.Command{
		Use:   "logs <tag>",
		Short: "Tail the build log of a detached job",
		Args:  cobra.ExactArgs(1),
		RunE:  runLogs,
	}
	logsCmd.Flags().StringVar(&logsPlatform, "platform", string(mini.PlatformMacOSArm64), "Platform to tail logs for")
	rootCmd.AddCommand(logsCmd)
}

func runLogs(cmd *cobra.Command, args []string) error {
	tag := args[0]
	j, err := findJob(tag, logsPlatform)
	if err != nil {
		return err
	}
	b := mini.NewBuilder(mini.Config{SSHHost: j.SSHHost, SSHPort: j.SSHPort})
	return b.TailLog(cmd.Context(), j.RemoteDir)
}

// ── fetch ─────────────────────────────────────────────────────────────────────

var fetchCmd = &cobra.Command{
	Use:   "fetch <tag>",
	Short: "Download finished detached builds and publish the GitHub release",
	Args:  cobra.ExactArgs(1),
	RunE:  runFetch,
}

func init() {
	rootCmd.AddCommand(fetchCmd)
}

func runFetch(cmd *cobra.Command, args []string) error {
	tag := args[0]

	jobs, err := mini.LoadJobs()
	if err != nil {
		return fmt.Errorf("load jobs: %w", err)
	}

	var matching []mini.Job
	for _, j := range jobs {
		if j.Tag == tag {
			matching = append(matching, j)
		}
	}
	if len(matching) == 0 {
		return fmt.Errorf("no jobs found for tag %q", tag)
	}

	// Check all are done before proceeding.
	for _, j := range matching {
		b := mini.NewBuilder(mini.Config{SSHHost: j.SSHHost, SSHPort: j.SSHPort})
		status, err := b.JobStatus(cmd.Context(), j.RemoteDir)
		if err != nil {
			return fmt.Errorf("job status [%s]: %w", j.Platform, err)
		}
		if !strings.HasPrefix(status, "done:") {
			return fmt.Errorf("job [%s] is still %s — wait for it to complete", j.Platform, status)
		}
		if status != "done:0" {
			code := strings.TrimPrefix(status, "done:")
			return fmt.Errorf("job [%s] failed with exit code %s", j.Platform, code)
		}
	}

	// Use config from first job (all platforms share same release config).
	first := matching[0]
	noRelease := first.NoRelease
	ghRepo := first.GHRepo
	suffix := first.Suffix

	outDir := "./dist"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}

	// Download binaries.
	var localPaths []string
	var platNames []string
	for _, j := range matching {
		b := mini.NewBuilder(mini.Config{SSHHost: j.SSHHost, SSHPort: j.SSHPort})

		remoteBinPath, err := b.ReadBinaryPath(cmd.Context(), j.RemoteDir)
		if err != nil {
			return fmt.Errorf("read binary path [%s]: %w", j.Platform, err)
		}
		if remoteBinPath == "" {
			return fmt.Errorf("binary path is empty for [%s]", j.Platform)
		}

		pattern := fmt.Sprintf("envoy-%s%s", j.Platform, suffix)
		localPath := fmt.Sprintf("%s/%s", strings.TrimRight(outDir, "/"), pattern)

		header("Downloading %s [%s]", tag, j.Platform)
		if err := b.Download(cmd.Context(), remoteBinPath, localPath); err != nil {
			return fmt.Errorf("download [%s]: %w", j.Platform, err)
		}
		okf("Binary: %s", localPath)

		// Write params JSON alongside the binary.
		params := buildParams{
			Platform: j.Platform,
			BuiltAt:  time.Now().UTC().Format(time.RFC3339),
		}
		paramsPath := localPath + ".params.json"
		if data, err := json.MarshalIndent(params, "", "  "); err == nil {
			_ = os.WriteFile(paramsPath, data, 0o644)
		}

		localPaths = append(localPaths, localPath, paramsPath)
		platNames = append(platNames, j.Platform)
	}

	// Publish release.
	if !noRelease {
		header("Ensure release %s", tag)
		body := releaseBody("", tag, "", platNames)
		if err := ghEnsureRelease(ghRepo, tag, body); err != nil {
			return fmt.Errorf("ensure release: %w", err)
		}
		okf("Release: %s", tag)

		for _, lp := range localPaths {
			if err := ghUploadAsset(ghRepo, tag, lp); err != nil {
				return fmt.Errorf("upload asset %s: %w", lp, err)
			}
			okf("Asset uploaded: %s", lp)
		}

		header("Publish release")
		if err := ghPublishRelease(ghRepo, tag); err != nil {
			return fmt.Errorf("publish release: %w", err)
		}
		okf("Release published: https://github.com/%s/releases/tag/%s", ghRepo, tag)
	}

	// Clean up local job state.
	for _, j := range matching {
		if err := mini.RemoveJob(j.Tag, j.Platform); err != nil {
			warnf("could not remove job state for [%s]: %v", j.Platform, err)
		}
	}

	header("Done")
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func findJob(tag, platform string) (*mini.Job, error) {
	jobs, err := mini.LoadJobs()
	if err != nil {
		return nil, fmt.Errorf("load jobs: %w", err)
	}
	for _, j := range jobs {
		if j.Tag == tag && j.Platform == platform {
			return &j, nil
		}
	}
	return nil, fmt.Errorf("no job found for tag=%q platform=%q", tag, platform)
}
