package manager

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var downloadClient *http.Client

func init() {
	transport := &http.Transport{
		Proxy: nil,
	}
	if p := os.Getenv("MIHOMO_DOWNLOAD_PROXY"); p != "" {
		if parsed, err := url.Parse(p); err == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	downloadClient = &http.Client{
		Transport: transport,
		Timeout:   10 * time.Minute,
	}
}

type FileSystem interface {
	FileExists(path string) bool
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm uint32) error
	Remove(path string) error
	Rename(oldPath, newPath string) error
	MkdirAll(path string, perm uint32) error
	Chmod(path string, perm uint32) error
}

type CommandRunner interface {
	RunCommand(ctx context.Context, name string, args ...string) (string, error)
	RunCommandIgnoreExit(ctx context.Context, name string, args ...string) (string, error)
}

type ReleaseSource interface {
	Download(ctx context.Context, url, dest string) error
	ExpectedChecksum(ctx context.Context, owner, repo, version, assetName string) (string, error)
	ListVersions(ctx context.Context, owner, repo string, limit int) ([]VersionInfo, error)
	LatestVersion(ctx context.Context, owner, repo string) (string, error)
}

// OSSystem is the thin production adapter for FileSystem, CommandRunner, and
// ReleaseSource. Keep these methods as stdlib/network boundary adapters only.
// Do not add a fourth responsibility here; introduce a separate seam for new
// capabilities such as process inspection, archive handling, or system info.
type OSSystem struct{}

func (OSSystem) FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (OSSystem) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (OSSystem) WriteFile(path string, data []byte, perm uint32) error {
	return os.WriteFile(path, data, os.FileMode(perm))
}

func (OSSystem) Remove(path string) error {
	return os.RemoveAll(path)
}

func (OSSystem) Rename(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func (OSSystem) MkdirAll(path string, perm uint32) error {
	return os.MkdirAll(path, os.FileMode(perm))
}

func (OSSystem) Chmod(path string, perm uint32) error {
	return os.Chmod(path, os.FileMode(perm))
}

func (OSSystem) RunCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return strings.TrimSpace(string(out)), nil
}

func (OSSystem) RunCommandIgnoreExit(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if _, ok := err.(*exec.ExitError); ok {
			return string(out), nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (OSSystem) Download(ctx context.Context, rawURL, dest string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: status %d", rawURL, resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", dest, err)
	}
	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("creating %s: %w", dest, err)
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		os.Remove(dest)
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	return nil
}

func normalizeChecksum(raw, assetName string) (string, error) {
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) > 1 {
			name := strings.TrimPrefix(fields[1], "*")
			if name != assetName {
				continue
			}
		}
		candidate := strings.TrimPrefix(fields[0], "sha256:")
		if len(candidate) != 64 {
			continue
		}
		decoded, err := hex.DecodeString(candidate)
		if err != nil || len(decoded) != 32 {
			continue
		}
		return strings.ToLower(candidate), nil
	}
	return "", fmt.Errorf("checksum for %s not found or invalid", assetName)
}

func (OSSystem) ExpectedChecksum(ctx context.Context, owner, repo, version, assetName string) (string, error) {
	if os.Getenv("MIHOMO_RELEASE_URL") != "" {
		checksumURL := releaseChecksumURL(version, assetName)
		if checksumURL == "" {
			return "", fmt.Errorf("MIHOMO_RELEASE_CHECKSUM_URL is required with MIHOMO_RELEASE_URL")
		}
		data, err := fetchChecksumURL(ctx, checksumURL)
		if err != nil {
			return "", err
		}
		return normalizeChecksum(string(data), assetName)
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", owner, repo, version)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := downloadClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching release checksum: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API: status %d", resp.StatusCode)
	}
	var release struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("decoding release checksum: %w", err)
	}
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			return normalizeChecksum(asset.Digest, assetName)
		}
	}
	return "", fmt.Errorf("release asset %s has no checksum", assetName)
}

func fetchChecksumURL(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating checksum request: %w", err)
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading checksum: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading checksum: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading checksum: %w", err)
	}
	return data, nil
}

func (OSSystem) ListVersions(ctx context.Context, owner, repo string, limit int) ([]VersionInfo, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=%d", owner, repo, limit)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := downloadClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API: status %d", resp.StatusCode)
	}
	var releases []struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("decoding releases: %w", err)
	}
	versions := make([]VersionInfo, 0, len(releases))
	for _, r := range releases {
		versions = append(versions, VersionInfo{Tag: r.TagName})
	}
	return versions, nil
}

func (OSSystem) LatestVersion(ctx context.Context, owner, repo string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := downloadClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API: status %d", resp.StatusCode)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("decoding release: %w", err)
	}
	return release.TagName, nil
}
