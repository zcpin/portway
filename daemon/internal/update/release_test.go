package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func TestSemanticVersionOrder(t *testing.T) {
	ordered := []string{"v1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.10.0", "2.0.0", "999999999999999999999999.0.0"}
	for i := 1; i < len(ordered); i++ {
		a, _ := parseVersion(ordered[i-1])
		b, _ := parseVersion(ordered[i])
		if a.compare(b) >= 0 || b.compare(a) <= 0 {
			t.Fatalf("wrong semantic order: %s, %s", ordered[i-1], ordered[i])
		}
	}
	a, _ := parseVersion("v1.0.0+build-rc.1")
	b, _ := parseVersion("1.0.0+different")
	if a.compare(b) != 0 || len(a.pre) != 0 {
		t.Fatal("build metadata changed version precedence or release channel")
	}
	for _, bad := range []string{"dev", "ci-123", "1.2", "01.2.3", "1.2.3-01", "1.2.3-", "1.2.3+", "1.2.3-a..b", "v1.2.3/evil", "1.2.3\n"} {
		if _, err := parseVersion(bad); err == nil {
			t.Errorf("accepted invalid version %q", bad)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func jsonResponse(value any) *http.Response {
	data, _ := json.Marshal(value)
	return response(string(data))
}

func response(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func testRelease(c *ReleaseClient, tag string, content string) (release, string) {
	portable, installer, sums := c.assetNames(tag)
	h := sha256.Sum256([]byte(content))
	checksum := hex.EncodeToString(h[:])
	lines := checksum + "  " + portable + "\n"
	if installer != "" {
		lines += checksum + "  " + installer + "\n"
	}
	assets := []Asset{}
	for _, name := range []string{portable, installer, sums} {
		if name == "" {
			continue
		}
		size := len(content)
		if name == sums {
			size = len(lines)
		}
		assets = append(assets, Asset{name, "https://github.com/" + c.Repository + "/releases/download/" + url.PathEscape(tag) + "/" + url.PathEscape(name), int64(size)})
	}
	return release{Tag: tag, Assets: assets}, lines
}

func TestCheckChannelsAndPlatforms(t *testing.T) {
	for _, platform := range []struct{ os, arch string }{{"windows", "amd64"}, {"linux", "amd64"}, {"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "arm64"}} {
		t.Run(platform.os+"-"+platform.arch, func(t *testing.T) {
			c, _ := NewReleaseClient("owner/repo", platform.os, platform.arch)
			stable, _ := testRelease(c, "v1.10.0+build-rc.1", "package")
			pre, _ := testRelease(c, "v2.0.0-rc.10", "package")
			flagged, _ := testRelease(c, "v3.0.0", "package")
			flagged.Prerelease = true
			c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("X-Auth-Token") != "" || r.Header.Get("Authorization") != "" {
					t.Fatal("local credentials escaped into GitHub request")
				}
				return jsonResponse([]release{pre, stable, flagged, {Tag: "v90.0.0", Draft: true}, {Tag: "v99.0.0-01"}}), nil
			})
			result, err := c.Check(context.Background(), "v1.9.0", "stable")
			if err != nil || result.Release.Version != stable.Tag || !result.Available || !result.CurrentKnown {
				t.Fatalf("stable selection: %+v, %v", result, err)
			}
			supported := platform.os != "linux" || platform.arch == "amd64"
			if (result.Release.Portable != nil) != supported {
				t.Fatalf("wrong platform asset: %+v", result.Release)
			}
			result, err = c.Check(context.Background(), "v3.0.0+local", "prerelease")
			if err != nil || result.Release.Version != "v3.0.0" || result.Available {
				t.Fatalf("prerelease selection: %+v, %v", result, err)
			}
			result, err = c.Check(context.Background(), "dev", "stable")
			if err != nil || result.CurrentKnown {
				t.Fatalf("development build: %+v, %v", result, err)
			}
		})
	}
}

func TestDownloadVerification(t *testing.T) {
	for _, scenario := range []string{"success", "checksum mismatch", "duplicate checksum", "missing checksum", "wrong size", "network failure", "external asset", "draft"} {
		t.Run(scenario, func(t *testing.T) {
			c, _ := NewReleaseClient("owner/repo", "windows", "amd64")
			content := "verified release contents"
			r, sums := testRelease(c, "v2.0.0", content)
			switch scenario {
			case "checksum mismatch":
				content = strings.Repeat("x", len(content))
			case "duplicate checksum":
				sums += sums
			case "missing checksum":
				sums = ""
			case "wrong size":
				content += "too much"
			case "external asset":
				r.Assets[0].URL = "https://evil.example/package.zip"
			case "draft":
				r.Draft = true
			}
			c.HTTP.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case req.URL.Host == "api.github.com":
					return jsonResponse(r), nil
				case strings.HasSuffix(req.URL.Path, "SHA256SUMS-windows"):
					return response(sums), nil
				case scenario == "network failure":
					return nil, errors.New("connection interrupted")
				default:
					return response(content), nil
				}
			})
			dir := t.TempDir()
			result, err := c.Download(context.Background(), r.Tag, "portable", dir)
			if scenario == "success" {
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(result.Path)
				if err != nil || string(data) != content || result.Repository != "owner/repo" {
					t.Fatalf("bad download: %+v, %v", result, err)
				}
				if err := verifyFile(result.Path, result.SHA256); err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("invalid download was accepted")
				}
				files, _ := filepath.Glob(filepath.Join(dir, "*", r.Assets[0].Name))
				if len(files) != 0 {
					t.Fatal("failed download was promoted to a usable package")
				}
			}
		})
	}
}

func TestUntrustedReleaseAddresses(t *testing.T) {
	for _, repo := range []string{"../other", "owner/../other", "https://github.com/owner/repo", "owner/repo?x=y", "owner/.."} {
		if _, err := NewReleaseClient(repo, "linux", "amd64"); err == nil {
			t.Errorf("accepted repository %q", repo)
		}
	}
	for _, address := range []string{"http://github.com/file", "https://github.com.evil.test/file", "https://user@github.com/file", "https://github.com:444/file", "file:///C:/file"} {
		req, _ := http.NewRequest(http.MethodGet, address, nil)
		if err := safeRedirect(req, nil); err == nil {
			t.Errorf("accepted redirect %q", address)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://release-assets.githubusercontent.com/file?signature=opaque", nil)
	if err := safeRedirect(req, nil); err != nil {
		t.Fatal(err)
	}
	if err := safeRedirect(req, make([]*http.Request, 5)); err == nil {
		t.Fatal("unbounded redirect chain")
	}
}
