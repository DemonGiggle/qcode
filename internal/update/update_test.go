package update

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParseVersionAndCompare(t *testing.T) {
	tests := []struct {
		name   string
		left   string
		right  string
		result int
		valid  bool
	}{
		{name: "equal with v prefix", left: "v1.2.3", right: "1.2.3", result: 0, valid: true},
		{name: "two component release tags", left: "v0.6", right: "v0.7", result: -1, valid: true},
		{name: "patch update", left: "1.2.3", right: "1.2.4", result: -1, valid: true},
		{name: "major downgrade", left: "2.0.0", right: "1.9.9", result: 1, valid: true},
		{name: "release beats prerelease", left: "1.2.3-rc.1", right: "1.2.3", result: -1, valid: true},
		{name: "dev is not semver", left: "dev", right: "1.2.3", valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left, leftOK := parseVersion(test.left)
			right, rightOK := parseVersion(test.right)
			if leftOK != test.valid || !rightOK {
				t.Fatalf("parse validity = %v/%v, want %v/true", leftOK, rightOK, test.valid)
			}
			if test.valid && compareVersions(left, right) != test.result {
				t.Fatalf("compareVersions = %d, want %d", compareVersions(left, right), test.result)
			}
		})
	}
}

func TestTargetPlatform(t *testing.T) {
	tests := []struct {
		name     string
		spec     string
		hostOS   string
		hostArch string
		wantOS   string
		wantArch string
		wantErr  string
	}{
		{name: "host default", hostOS: "linux", hostArch: "amd64", wantOS: "linux", wantArch: "amd64"},
		{name: "architecture override", spec: "arm64", hostOS: "linux", hostArch: "amd64", wantOS: "linux", wantArch: "arm64"},
		{name: "full platform override", spec: "linux/aarch64", hostOS: "linux", hostArch: "amd64", wantOS: "linux", wantArch: "arm64"},
		{name: "different OS rejected", spec: "darwin_arm64", hostOS: "linux", hostArch: "amd64", wantErr: "does not match"},
		{name: "unsupported architecture", spec: "386", hostOS: "linux", hostArch: "amd64", wantErr: "unsupported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotOS, gotArch, err := targetPlatform(test.spec, test.hostOS, test.hostArch)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil || gotOS != test.wantOS || gotArch != test.wantArch {
				t.Fatalf("target = %s/%s, error = %v; want %s/%s", gotOS, gotArch, err, test.wantOS, test.wantArch)
			}
		})
	}
}

func TestFindAsset(t *testing.T) {
	assets := []asset{
		{Name: "qcode_linux_amd64"},
		{Name: "qcode_windows_amd64.exe"},
	}
	if got, ok := findAsset(assets, "windows", "amd64"); !ok || got.Name != "qcode_windows_amd64.exe" {
		t.Fatalf("windows asset = %+v, %v", got, ok)
	}
	if _, ok := findAsset(assets, "darwin", "arm64"); ok {
		t.Fatal("found an asset that was not present")
	}
}

func TestRunDownloadsAndReplacesExecutable(t *testing.T) {
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "qcode")
	if err := os.WriteFile(target, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}

	var downloads atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/latest":
			return fakeResponse(http.StatusOK, `{"tag_name":"v0.7","assets":[{"name":"qcode_linux_amd64","browser_download_url":"https://api.test/download/linux"},{"name":"qcode_linux_arm64","browser_download_url":"https://api.test/download/arm64"}]}`), nil
		case "/download/linux":
			downloads.Add(1)
			return fakeResponse(http.StatusOK, "new binary"), nil
		default:
			return fakeResponse(http.StatusNotFound, "not found"), nil
		}
	})}

	var output strings.Builder
	err := Run(context.Background(), nil, Options{
		CurrentVersion:       "v0.6",
		GOOS:                 "linux",
		GOARCH:               "amd64",
		ExecutablePath:       target,
		ReleaseURL:           "https://api.test/latest",
		Client:               client,
		Stdout:               &output,
		DownloadURLValidator: func(string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new binary" || downloads.Load() != 1 {
		t.Fatalf("content = %q, downloads = %d", content, downloads.Load())
	}
	if !strings.Contains(output.String(), "from v0.6 to v0.7") || !strings.Contains(output.String(), target) {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunSkipsDownloadWhenCurrentVersionMatches(t *testing.T) {
	var downloads atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/latest" {
			return fakeResponse(http.StatusOK, `{"tag_name":"1.1.0","assets":[]}`), nil
		}
		downloads.Add(1)
		return fakeResponse(http.StatusNotFound, "not found"), nil
	})}

	var output strings.Builder
	err := Run(context.Background(), nil, Options{
		CurrentVersion: "v1.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		ReleaseURL:     "https://api.test/latest",
		Client:         client,
		Stdout:         &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 0 || !strings.Contains(output.String(), "already up to date") {
		t.Fatalf("downloads = %d, output = %q", downloads.Load(), output.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func fakeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestValidateDownloadURL(t *testing.T) {
	if err := validateDownloadURL("https://github.com/DemonGiggle/qcode/releases/download/v1.2.3/qcode_linux_amd64"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"http://github.com/DemonGiggle/qcode/releases/download/v1.2.3/qcode_linux_amd64",
		"https://example.com/DemonGiggle/qcode/releases/download/v1.2.3/qcode_linux_amd64",
		"https://github.com/other/repo/releases/download/v1.2.3/qcode_linux_amd64",
	} {
		if err := validateDownloadURL(raw); err == nil {
			t.Fatalf("validateDownloadURL(%q) unexpectedly succeeded", raw)
		}
	}
}
