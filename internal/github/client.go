package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const apiBase = "https://api.github.com"

// Client is a minimal GitHub REST client. No generics, no reflection —
// just typed request/response structs and direct HTTP calls.
type Client struct {
	token string
	http  *http.Client
}

func NewClient(token string) *Client {
	return &Client{token: token, http: &http.Client{}}
}

func (c *Client) do(method, url string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

func (c *Client) checkStatus(resp *http.Response, wantStatus int) error {
	if resp.StatusCode == wantStatus {
		return nil
	}
	b, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("GitHub API %s: status %d: %s", resp.Request.URL, resp.StatusCode, string(b))
}

type createReleaseRequest struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Body    string `json:"body"`
	Draft   bool   `json:"draft"`
}

type releaseResponse struct {
	ID int64 `json:"id"`
}

// CreateDraftRelease creates a draft release and returns its numeric ID.
func (c *Client) CreateDraftRelease(repo, tag, body string) (int64, error) {
	url := fmt.Sprintf("%s/repos/%s/releases", apiBase, repo)
	resp, err := c.do("POST", url, createReleaseRequest{
		TagName: tag,
		Name:    tag,
		Body:    body,
		Draft:   true,
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if err := c.checkStatus(resp, http.StatusCreated); err != nil {
		return 0, err
	}
	var r releaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return 0, err
	}
	return r.ID, nil
}

// UploadAsset uploads a local file to an existing release.
func (c *Client) UploadAsset(repo string, releaseID int64, name, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	url := fmt.Sprintf("https://uploads.github.com/repos/%s/releases/%d/assets?name=%s", repo, releaseID, name)
	req, err := http.NewRequest("POST", url, f)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return c.checkStatus(resp, http.StatusCreated)
}

type updateReleaseRequest struct {
	Name       string `json:"name"`
	Body       string `json:"body,omitempty"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// publishReleaseRequest contains only the fields we want to patch when
// publishing. Sending Name="" or Prerelease=false as zero values would
// overwrite the name set at creation and force prerelease=false even when
// the release was created as a prerelease.
type publishReleaseRequest struct {
	Draft bool `json:"draft"`
}

// PublishRelease marks a draft release as published.
func (c *Client) PublishRelease(repo string, releaseID int64) error {
	url := fmt.Sprintf("%s/repos/%s/releases/%d", apiBase, repo, releaseID)
	resp, err := c.do("PATCH", url, publishReleaseRequest{Draft: false})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return c.checkStatus(resp, http.StatusOK)
}

// MarkReleaseFailed marks a draft release as a failed prerelease.
func (c *Client) MarkReleaseFailed(repo string, releaseID int64, tag string) error {
	url := fmt.Sprintf("%s/repos/%s/releases/%d", apiBase, repo, releaseID)
	resp, err := c.do("PATCH", url, updateReleaseRequest{
		Name:       strings.TrimSuffix(tag, " [FAILED]") + " [FAILED]",
		Body:       "Build failed. Check the CLI output for details.",
		Draft:      false,
		Prerelease: true,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return c.checkStatus(resp, http.StatusOK)
}
