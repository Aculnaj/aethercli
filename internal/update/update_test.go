package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestIsNewerVersionComparesReleaseTags(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "newer patch with v prefix", current: "v1.2.3", latest: "v1.2.4", want: true},
		{name: "newer minor without v prefix", current: "1.2.3", latest: "1.3.0", want: true},
		{name: "same version", current: "v1.2.3", latest: "v1.2.3", want: false},
		{name: "older latest", current: "v1.2.3", latest: "v1.2.2", want: false},
		{name: "development build skips checks", current: "dev", latest: "v1.2.4", want: false},
		{name: "invalid latest skips checks", current: "v1.2.3", latest: "next", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNewerVersion(tt.current, tt.latest); got != tt.want {
				t.Fatalf("IsNewerVersion(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestGitHubCheckerLatestRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/Aculnaj/aethercli/releases/latest" {
			t.Fatalf("path = %q, want latest release endpoint", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"tag_name":"v1.2.4","html_url":"https://example.test/release"}`)
	}))
	defer server.Close()

	checker := GitHubChecker{
		BaseURL: server.URL,
		Repo:    "Aculnaj/aethercli",
		Client:  server.Client(),
	}

	release, err := checker.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest returned error: %v", err)
	}
	if release.Version != "v1.2.4" {
		t.Fatalf("Version = %q, want v1.2.4", release.Version)
	}
	if release.URL != "https://example.test/release" {
		t.Fatalf("URL = %q, want release URL", release.URL)
	}
}

func TestBinaryInstallerDownloadsAndInstallsArchiveAsset(t *testing.T) {
	archive := tarGzWithFile(t, "aether", []byte("#!/bin/sh\necho updated\n"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Aculnaj/aethercli/releases/download/v1.2.4/aether_darwin_arm64.tar.gz" {
			t.Fatalf("path = %q, want release asset path", r.URL.Path)
		}
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	installDir := t.TempDir()
	installer := BinaryInstaller{
		AssetBaseURL: server.URL,
		Repo:         "Aculnaj/aethercli",
		Client:       server.Client(),
		GOOS:         "darwin",
		GOARCH:       "arm64",
	}

	result, err := installer.Install(context.Background(), InstallOptions{
		Version:    "v1.2.4",
		InstallDir: installDir,
	})
	if err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	if result.Path != filepath.Join(installDir, "aether") {
		t.Fatalf("Path = %q, want installed binary path", result.Path)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if string(data) != "#!/bin/sh\necho updated\n" {
		t.Fatalf("installed content = %q", data)
	}
	info, err := os.Stat(result.Path)
	if err != nil {
		t.Fatalf("stat installed binary: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}
}

func tarGzWithFile(t *testing.T, name string, data []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	gzipWriter := gzip.NewWriter(&buf)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o755,
		Size: int64(len(data)),
	}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tarWriter.Write(data); err != nil {
		t.Fatalf("write tar body: %v", err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return buf.Bytes()
}
