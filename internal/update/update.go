package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const (
	DefaultRepo         = "Aculnaj/aethercli"
	defaultAPIBaseURL   = "https://api.github.com"
	defaultAssetBaseURL = "https://github.com"
)

var releaseVersionPattern = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+){0,2}([-.+][0-9A-Za-z.-]+)?$`)

type Release struct {
	Version string
	URL     string
}

type Checker interface {
	Latest(ctx context.Context) (Release, error)
}

type Installer interface {
	Install(ctx context.Context, options InstallOptions) (InstallResult, error)
}

type InstallOptions struct {
	Version    string
	InstallDir string
}

type InstallResult struct {
	Path string
}

type GitHubChecker struct {
	BaseURL string
	Repo    string
	Client  *http.Client
}

func (c GitHubChecker) Latest(ctx context.Context) (Release, error) {
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultAPIBaseURL
	}
	repo := c.Repo
	if repo == "" {
		repo = DefaultRepo
	}
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}

	endpoint := fmt.Sprintf("%s/repos/%s/releases/latest", baseURL, strings.Trim(repo, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "aether-cli")

	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return Release{}, fmt.Errorf("latest release request failed: %s", resp.Status)
	}

	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Release{}, err
	}
	if strings.TrimSpace(payload.TagName) == "" {
		return Release{}, errors.New("latest release response did not include tag_name")
	}
	return Release{Version: strings.TrimSpace(payload.TagName), URL: strings.TrimSpace(payload.HTMLURL)}, nil
}

type BinaryInstaller struct {
	AssetBaseURL string
	Repo         string
	Client       *http.Client
	GOOS         string
	GOARCH       string
}

func NewDefaultChecker() Checker {
	return GitHubChecker{Repo: DefaultRepo}
}

func NewDefaultInstaller() Installer {
	return BinaryInstaller{Repo: DefaultRepo}
}

func (i BinaryInstaller) Install(ctx context.Context, options InstallOptions) (InstallResult, error) {
	version := strings.TrimSpace(options.Version)
	if version == "" {
		version = "latest"
	}
	if version != "latest" && !IsSafeReleaseVersion(version) {
		return InstallResult{}, fmt.Errorf("unsafe release version %q", version)
	}

	goos := i.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := i.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}

	assetName, binaryName, err := assetNames(goos, goarch)
	if err != nil {
		return InstallResult{}, err
	}
	assetURL := i.assetURL(version, assetName)

	client := i.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return InstallResult{}, err
	}
	req.Header.Set("User-Agent", "aether-cli")

	resp, err := client.Do(req)
	if err != nil {
		return InstallResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return InstallResult{}, fmt.Errorf("download failed: %s", resp.Status)
	}
	archiveData, err := io.ReadAll(resp.Body)
	if err != nil {
		return InstallResult{}, err
	}

	binaryData, err := extractBinary(archiveData, assetName, binaryName)
	if err != nil {
		return InstallResult{}, err
	}

	installDir := strings.TrimSpace(options.InstallDir)
	if installDir == "" {
		installDir, err = defaultInstallDir()
		if err != nil {
			return InstallResult{}, err
		}
	}
	targetPath := filepath.Join(installDir, binaryName)
	if err := writeExecutable(targetPath, binaryData); err != nil {
		return InstallResult{}, err
	}
	return InstallResult{Path: targetPath}, nil
}

func (i BinaryInstaller) assetURL(version, assetName string) string {
	baseURL := strings.TrimRight(i.AssetBaseURL, "/")
	if baseURL == "" {
		baseURL = defaultAssetBaseURL
	}
	repo := strings.Trim(i.Repo, "/")
	if repo == "" {
		repo = DefaultRepo
	}
	if version == "latest" {
		return fmt.Sprintf("%s/%s/releases/latest/download/%s", baseURL, repo, url.PathEscape(assetName))
	}
	return fmt.Sprintf("%s/%s/releases/download/%s/%s", baseURL, repo, url.PathEscape(version), url.PathEscape(assetName))
}

func IsNewerVersion(current, latest string) bool {
	currentParts, ok := parseVersion(current)
	if !ok {
		return false
	}
	latestParts, ok := parseVersion(latest)
	if !ok {
		return false
	}
	for i := range currentParts {
		if latestParts[i] > currentParts[i] {
			return true
		}
		if latestParts[i] < currentParts[i] {
			return false
		}
	}
	return false
}

func IsSafeReleaseVersion(version string) bool {
	return releaseVersionPattern.MatchString(strings.TrimSpace(version))
}

func parseVersion(version string) ([3]int, bool) {
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(version, "v")
	version = strings.TrimPrefix(version, "V")
	if version == "" || strings.EqualFold(version, "dev") {
		return [3]int{}, false
	}
	if idx := strings.IndexAny(version, "-+"); idx >= 0 {
		version = version[:idx]
	}
	parts := strings.Split(version, ".")
	if len(parts) > 3 {
		return [3]int{}, false
	}
	var parsed [3]int
	for i, part := range parts {
		if part == "" {
			return [3]int{}, false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		parsed[i] = n
	}
	return parsed, true
}

func assetNames(goos, goarch string) (string, string, error) {
	switch goos {
	case "darwin", "linux":
	case "windows":
	default:
		return "", "", fmt.Errorf("unsupported operating system: %s", goos)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", "", fmt.Errorf("unsupported CPU architecture: %s", goarch)
	}

	binaryName := "aether"
	format := "tar.gz"
	if goos == "windows" {
		binaryName = "aether.exe"
		format = "zip"
	}
	return fmt.Sprintf("aether_%s_%s.%s", goos, goarch, format), binaryName, nil
}

func extractBinary(data []byte, assetName, binaryName string) ([]byte, error) {
	if strings.HasSuffix(assetName, ".zip") {
		return extractBinaryFromZip(data, binaryName)
	}
	return extractBinaryFromTarGz(data, binaryName)
}

func extractBinaryFromTarGz(data []byte, binaryName string) ([]byte, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != binaryName {
			continue
		}
		return io.ReadAll(tarReader)
	}
	return nil, fmt.Errorf("archive did not contain %s", binaryName)
}

func extractBinaryFromZip(data []byte, binaryName string) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, file := range reader.File {
		if filepath.Base(file.Name) != binaryName {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return nil, fmt.Errorf("archive did not contain %s", binaryName)
}

func defaultInstallDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func writeExecutable(targetPath string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(targetPath), ".aether-update-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Chmod(0o755); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}
