package mini

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Job holds the state of a detached remote build.
type Job struct {
	Tag       string    `json:"tag"`
	Platform  string    `json:"platform"`
	SSHHost   string    `json:"ssh_host"`
	SSHPort   int       `json:"ssh_port"`
	RemoteDir string    `json:"remote_dir"` // absolute path on the mini
	GHRepo    string    `json:"gh_repo"`
	Suffix    string    `json:"suffix"`
	NoRelease bool      `json:"no_release"`
	StartedAt time.Time `json:"started_at"`
}

// JobsDir returns the directory where job state files are stored.
func JobsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".envoy-mini-builder", "jobs"), nil
}

// SaveJob writes job state to disk, creating the jobs directory if needed.
func SaveJob(job Job) error {
	dir, err := JobsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create jobs dir: %w", err)
	}
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}
	name := fmt.Sprintf("%s-%s.json", job.Tag, job.Platform)
	path := filepath.Join(dir, name)
	return os.WriteFile(path, data, 0o644)
}

// LoadJobs reads all job state files from the jobs directory.
func LoadJobs() ([]Job, error) {
	dir, err := JobsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read jobs dir: %w", err)
	}
	var jobs []Job
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read job %s: %w", e.Name(), err)
		}
		var j Job
		if err := json.Unmarshal(data, &j); err != nil {
			return nil, fmt.Errorf("parse job %s: %w", e.Name(), err)
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// RemoveJob deletes the state file for the given tag+platform combination.
func RemoveJob(tag, platform string) error {
	dir, err := JobsDir()
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%s.json", tag, platform)
	path := filepath.Join(dir, name)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove job %s: %w", name, err)
	}
	return nil
}
