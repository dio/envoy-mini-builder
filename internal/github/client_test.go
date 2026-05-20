package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestClientCreateDraftRelease(t *testing.T) {
	var gotBody createReleaseRequest
	c := &Client{
		token: "token-123",
		http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", req.Method)
			}
			if req.URL.Path != "/repos/dio/envoy-builder/releases" {
				t.Fatalf("path = %s, want release create path", req.URL.Path)
			}
			assertGitHubHeaders(t, req, "application/json")
			if err := json.NewDecoder(req.Body).Decode(&gotBody); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			return jsonResponse(req, http.StatusCreated, `{"id":12345}`), nil
		})},
	}

	id, err := c.CreateDraftRelease("dio/envoy-builder", "envoy-abc", "body text")
	if err != nil {
		t.Fatalf("CreateDraftRelease returned error: %v", err)
	}
	if id != 12345 {
		t.Fatalf("release id = %d, want 12345", id)
	}
	want := createReleaseRequest{TagName: "envoy-abc", Name: "envoy-abc", Body: "body text", Draft: true}
	if gotBody != want {
		t.Fatalf("request body = %#v, want %#v", gotBody, want)
	}
}

func TestClientUploadAsset(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "envoy")
	if err := os.WriteFile(localPath, []byte("asset-bytes"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	c := &Client{
		token: "token-123",
		http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", req.Method)
			}
			if req.URL.Host != "uploads.github.com" {
				t.Fatalf("host = %s, want uploads.github.com", req.URL.Host)
			}
			if req.URL.Path != "/repos/dio/envoy-builder/releases/99/assets" {
				t.Fatalf("path = %s, want asset upload path", req.URL.Path)
			}
			if req.URL.Query().Get("name") != "envoy-macos-arm64" {
				t.Fatalf("asset name query = %q", req.URL.RawQuery)
			}
			assertGitHubHeaders(t, req, "application/octet-stream")
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			if string(body) != "asset-bytes" {
				t.Fatalf("request body = %q, want asset bytes", body)
			}
			return jsonResponse(req, http.StatusCreated, `{}`), nil
		})},
	}

	if err := c.UploadAsset("dio/envoy-builder", 99, "envoy-macos-arm64", localPath); err != nil {
		t.Fatalf("UploadAsset returned error: %v", err)
	}
}

func TestClientUploadAssetNonCreated(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "envoy")
	if err := os.WriteFile(localPath, []byte("asset"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	c := &Client{
		token: "token-123",
		http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(req, http.StatusBadRequest, `{"message":"bad asset"}`), nil
		})},
	}

	err := c.UploadAsset("dio/envoy-builder", 99, "envoy", localPath)
	if err == nil || !strings.Contains(err.Error(), "status 400") || !strings.Contains(err.Error(), "bad asset") {
		t.Fatalf("UploadAsset error = %v, want status/body", err)
	}
}

func TestClientPublishReleaseSendsDraftOnly(t *testing.T) {
	c := &Client{
		token: "token-123",
		http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPatch {
				t.Fatalf("method = %s, want PATCH", req.Method)
			}
			if req.URL.Path != "/repos/dio/envoy-builder/releases/99" {
				t.Fatalf("path = %s, want release update path", req.URL.Path)
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			if string(body) != `{"draft":false}`+"\n" && string(body) != `{"draft":false}` {
				t.Fatalf("body = %q, want draft-only patch", body)
			}
			if bytes.Contains(body, []byte("name")) || bytes.Contains(body, []byte("prerelease")) {
				t.Fatalf("body = %q, should not clobber name/prerelease", body)
			}
			return jsonResponse(req, http.StatusOK, `{}`), nil
		})},
	}

	if err := c.PublishRelease("dio/envoy-builder", 99); err != nil {
		t.Fatalf("PublishRelease returned error: %v", err)
	}
}

func TestClientMarkReleaseFailed(t *testing.T) {
	var got updateReleaseRequest
	c := &Client{
		token: "token-123",
		http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPatch {
				t.Fatalf("method = %s, want PATCH", req.Method)
			}
			if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			return jsonResponse(req, http.StatusOK, `{}`), nil
		})},
	}

	if err := c.MarkReleaseFailed("dio/envoy-builder", 99, "envoy-abc [FAILED]"); err != nil {
		t.Fatalf("MarkReleaseFailed returned error: %v", err)
	}
	want := updateReleaseRequest{
		Name:       "envoy-abc [FAILED]",
		Body:       "Build failed. Check the CLI output for details.",
		Draft:      false,
		Prerelease: true,
	}
	if got != want {
		t.Fatalf("request body = %#v, want %#v", got, want)
	}
}

func TestClientCheckStatusIncludesStatusAndBody(t *testing.T) {
	u, err := url.Parse("https://api.github.com/repos/dio/envoy-builder/releases")
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	resp := &http.Response{
		StatusCode: http.StatusTeapot,
		Body:       io.NopCloser(strings.NewReader(`{"message":"short and stout"}`)),
		Request:    &http.Request{URL: u},
	}

	c := &Client{}
	err = c.checkStatus(resp, http.StatusOK)
	if err == nil {
		t.Fatal("checkStatus succeeded, want error")
	}
	for _, want := range []string{"https://api.github.com/repos/dio/envoy-builder/releases", "status 418", "short and stout"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("checkStatus error %q missing %q", err, want)
		}
	}
}

func TestClientDoPropagatesTransportError(t *testing.T) {
	wantErr := errors.New("transport down")
	c := &Client{
		token: "token-123",
		http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, wantErr
		})},
	}

	_, err := c.CreateDraftRelease("dio/envoy-builder", "tag", "body")
	if !errors.Is(err, wantErr) {
		t.Fatalf("CreateDraftRelease error = %v, want %v", err, wantErr)
	}
}

func assertGitHubHeaders(t *testing.T, req *http.Request, contentType string) {
	t.Helper()
	headers := map[string]string{
		"Authorization":        "Bearer token-123",
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"Content-Type":         contentType,
	}
	for name, want := range headers {
		if got := req.Header.Get(name); got != want {
			t.Fatalf("%s header = %q, want %q", name, got, want)
		}
	}
}

func jsonResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}
}
