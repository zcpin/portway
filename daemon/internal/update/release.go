package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxDownload = 512 << 20

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$`)

func validateRepository(repository string) error {
	if !repositoryPattern.MatchString(repository) || strings.HasSuffix(repository, "/.") || strings.HasSuffix(repository, "/..") {
		return errors.New("GitHub repository must be owner/repository")
	}
	return nil
}

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type release struct {
	Tag        string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

type ReleaseInfo struct {
	Version    string `json:"version"`
	URL        string `json:"url"`
	Prerelease bool   `json:"prerelease"`
	Portable   *Asset `json:"portable"`
	Installer  *Asset `json:"installer"`
}

type CheckResult struct {
	CurrentVersion string       `json:"current_version"`
	CurrentKnown   bool         `json:"current_known"`
	Available      bool         `json:"available"`
	Release        *ReleaseInfo `json:"release"`
}

// Repository and asset hosts are constrained independently of the HTTP transport.
// Tests supply a transport, never an alternative production API/asset origin.
type ReleaseClient struct {
	Repository string
	OS         string
	Arch       string
	HTTP       *http.Client
}

func NewReleaseClient(repository, platform, arch string) (*ReleaseClient, error) {
	if err := validateRepository(repository); err != nil {
		return nil, err
	}
	return &ReleaseClient{
		Repository: repository, OS: platform, Arch: arch,
		HTTP: &http.Client{Timeout: 10 * time.Minute, CheckRedirect: safeRedirect},
	}, nil
}

func safeRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many download redirects")
	}
	u := req.URL
	if u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return errors.New("unsafe download redirect")
	}
	switch u.Hostname() {
	case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return nil
	default:
		return errors.New("download redirected outside GitHub")
	}
}

func (c *ReleaseClient) get(ctx context.Context, address string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "portway-updater")
	if req.URL.Hostname() == "api.github.com" {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GitHub returned HTTP %d (repository unavailable or request limit reached)", resp.StatusCode)
	}
	return resp, nil
}

func (c *ReleaseClient) read(ctx context.Context, address string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := c.get(ctx, address)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("release response exceeds size limit")
	}
	return data, err
}

func (c *ReleaseClient) Check(ctx context.Context, current, channel string) (CheckResult, error) {
	result := CheckResult{CurrentVersion: current}
	currentVersion, currentErr := parseVersion(current)
	result.CurrentKnown = currentErr == nil
	if channel != "stable" && channel != "prerelease" {
		return result, errors.New("channel must be stable or prerelease")
	}
	var selected *release
	var selectedVersion semVersion
	// Bound pagination; do not silently claim a complete result if the bound is hit.
	for page := 1; page <= 10; page++ {
		data, err := c.read(ctx, fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100&page=%d", c.Repository, page), 4<<20)
		if err != nil {
			return result, err
		}
		var releases []release
		if err := json.Unmarshal(data, &releases); err != nil {
			return result, fmt.Errorf("invalid release response: %w", err)
		}
		for _, candidate := range releases {
			v, err := parseVersion(candidate.Tag)
			if err != nil || candidate.Draft || !strings.HasPrefix(candidate.Tag, "v") {
				continue
			}
			if channel == "stable" && (candidate.Prerelease || len(v.pre) > 0) {
				continue
			}
			if selected == nil || v.compare(selectedVersion) > 0 {
				copy := candidate
				selected, selectedVersion = &copy, v
			}
		}
		if len(releases) < 100 {
			break
		}
		if page == 10 {
			return result, errors.New("release history exceeds the supported 1000 entries")
		}
	}
	if selected != nil {
		info := c.describe(*selected)
		result.Release = &info
		result.Available = currentErr != nil || selectedVersion.compare(currentVersion) > 0
	}
	return result, nil
}

func (c *ReleaseClient) assetNames(tag string) (portable, installer, checksum string) {
	switch {
	case c.OS == "windows" && c.Arch == "amd64":
		return "portway-portable-" + tag + ".zip", "portway-setup-" + tag + ".exe", "SHA256SUMS-windows"
	case c.OS == "linux" && c.Arch == "amd64":
		return "portway-portable-" + tag + "-linux-x64.tar.gz", "", "SHA256SUMS-linux"
	case c.OS == "darwin" && (c.Arch == "arm64" || c.Arch == "amd64"):
		return "portway-portable-" + tag + "-macos-" + c.Arch + ".zip", "", "SHA256SUMS-macos"
	default:
		return "", "", ""
	}
}

func (c *ReleaseClient) findAsset(r release, name string) *Asset {
	var found *Asset
	if name == "" {
		return nil
	}
	for _, a := range r.Assets {
		if a.Name != name {
			continue
		}
		expected := "https://github.com/" + c.Repository + "/releases/download/" + url.PathEscape(r.Tag) + "/" + url.PathEscape(name)
		u, err := url.Parse(a.URL)
		e, _ := url.Parse(expected)
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != e.Path || a.Size <= 0 || a.Size > maxDownload || found != nil {
			return nil
		}
		copy := a
		found = &copy
	}
	return found
}

func (c *ReleaseClient) describe(r release) ReleaseInfo {
	portable, installer, checksum := c.assetNames(r.Tag)
	v, _ := parseVersion(r.Tag)
	info := ReleaseInfo{Version: r.Tag, URL: "https://github.com/" + c.Repository + "/releases/tag/" + url.PathEscape(r.Tag), Prerelease: r.Prerelease || len(v.pre) > 0}
	if c.findAsset(r, checksum) != nil {
		info.Portable = c.findAsset(r, portable)
		info.Installer = c.findAsset(r, installer)
	}
	return info
}

type Download struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Version    string `json:"version"`
	Repository string `json:"repository"`
	Kind       string `json:"kind"`
}

func (c *ReleaseClient) Download(ctx context.Context, tag, kind, directory string) (Download, error) {
	var result Download
	if _, err := parseVersion(tag); err != nil || !strings.HasPrefix(tag, "v") {
		return result, errors.New("invalid release tag")
	}
	if kind != "portable" && kind != "installer" {
		return result, errors.New("package kind must be portable or installer")
	}
	data, err := c.read(ctx, "https://api.github.com/repos/"+c.Repository+"/releases/tags/"+url.PathEscape(tag), 4<<20)
	if err != nil {
		return result, err
	}
	var r release
	if err := json.Unmarshal(data, &r); err != nil || r.Draft || r.Tag != tag {
		return result, errors.New("release is missing, draft, or invalid")
	}
	portable, installer, checksum := c.assetNames(tag)
	name := portable
	if kind == "installer" {
		name = installer
	}
	asset, sums := c.findAsset(r, name), c.findAsset(r, checksum)
	if asset == nil || sums == nil {
		return result, errors.New("no matching package and checksum for this platform")
	}
	data, err = c.read(ctx, sums.URL, 1<<20)
	if err != nil {
		return result, err
	}
	want, err := checksumFor(string(data), name)
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return result, err
	}
	dir, err := os.MkdirTemp(directory, "download-")
	if err != nil {
		return result, err
	}
	partial := filepath.Join(dir, name+".partial")
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, err
	}
	defer file.Close()
	resp, err := c.get(ctx, asset.URL)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, asset.Size+1))
	if err != nil {
		return result, err
	}
	if n != asset.Size || hex.EncodeToString(hash.Sum(nil)) != want {
		return result, errors.New("download size or SHA256 mismatch; package was not accepted")
	}
	if err := file.Sync(); err != nil {
		return result, err
	}
	if err := file.Close(); err != nil {
		return result, err
	}
	path := filepath.Join(dir, name)
	if err := os.Rename(partial, path); err != nil {
		return result, err
	}
	return Download{Path: path, SHA256: want, Version: tag, Repository: c.Repository, Kind: kind}, nil
}

func checksumFor(contents, name string) (string, error) {
	result := ""
	for _, line := range strings.Split(contents, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size || result != "" {
			return "", errors.New("invalid or duplicate package checksum")
		}
		result = strings.ToLower(fields[0])
	}
	if result == "" {
		return "", errors.New("package is absent from checksum file")
	}
	return result, nil
}
