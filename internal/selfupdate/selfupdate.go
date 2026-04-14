// Package selfupdate checks for newer GitHub releases and replaces the
// running binary in-place.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	repo       = "vaughanb/codient"
	binaryName = "codient"
	apiTimeout = 4 * time.Second
)

// LatestVersion queries the GitHub releases API and returns the tag_name
// of the latest release (e.g. "v0.3.0"). A short timeout is used so the
// check does not noticeably delay startup.
func LatestVersion() (string, error) {
	url := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: apiTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github API: %s", resp.Status)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}
	if release.TagName == "" {
		return "", fmt.Errorf("empty tag_name in GitHub response")
	}
	return release.TagName, nil
}

// IsNewer reports whether latest (a tag like "v1.2.3") is newer than current.
func IsNewer(current, latest string) bool {
	cv, ok1 := parseSemver(current)
	lv, ok2 := parseSemver(latest)
	if !ok1 || !ok2 {
		return false
	}
	if lv[0] != cv[0] {
		return lv[0] > cv[0]
	}
	if lv[1] != cv[1] {
		return lv[1] > cv[1]
	}
	return lv[2] > cv[2]
}

// parseSemver strips an optional "v" prefix and parses "major.minor.patch".
func parseSemver(s string) ([3]int, bool) {
	s = strings.TrimPrefix(s, "v")
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

// Apply downloads the release archive for tag and replaces the running binary.
func Apply(tag string) error {
	version := strings.TrimPrefix(tag, "v")
	osName := runtime.GOOS
	arch := runtime.GOARCH

	ext := "tar.gz"
	if osName == "windows" {
		ext = "zip"
	}

	archive := fmt.Sprintf("%s_%s_%s_%s.%s", binaryName, version, osName, arch, ext)
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, archive)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", archive, resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}

	bin, err := extractBinary(data, ext)
	if err != nil {
		return err
	}

	return replaceBinary(bin)
}

// extractBinary pulls the codient binary out of a tar.gz or zip archive.
func extractBinary(data []byte, ext string) ([]byte, error) {
	if ext == "zip" {
		return extractZip(data)
	}
	return extractTarGz(data)
}

func extractTarGz(data []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		name := filepath.Base(hdr.Name)
		if name == binaryName || name == binaryName+".exe" {
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read %s from tar: %w", name, err)
			}
			return b, nil
		}
	}
	return nil, fmt.Errorf("binary %q not found in archive", binaryName)
}

func extractZip(data []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("zip: %w", err)
	}
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		if name == binaryName || name == binaryName+".exe" {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("open %s in zip: %w", name, err)
			}
			defer rc.Close()
			b, err := io.ReadAll(rc)
			if err != nil {
				return nil, fmt.Errorf("read %s from zip: %w", name, err)
			}
			return b, nil
		}
	}
	return nil, fmt.Errorf("binary %q not found in archive", binaryName)
}

// replaceBinary writes new binary data over the current executable using the
// write-to-temp-then-rename pattern for atomicity.
func replaceBinary(newBin []byte) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}

	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, binaryName+"-update-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(newBin); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod: %w", err)
	}

	if err := os.Rename(tmpPath, exe); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}

// skipFilePath returns the path to ~/.codient/update_skip.
func skipFilePath(stateDir string) string {
	return filepath.Join(stateDir, "update_skip")
}

// LoadSkippedVersion reads the version tag the user chose to skip.
// Returns "" if no skip file exists or on any error.
func LoadSkippedVersion(stateDir string) string {
	if stateDir == "" {
		return ""
	}
	data, err := os.ReadFile(skipFilePath(stateDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SaveSkippedVersion persists the version tag the user chose to skip.
func SaveSkippedVersion(stateDir, tag string) error {
	if stateDir == "" {
		return fmt.Errorf("no state directory")
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(skipFilePath(stateDir), []byte(tag+"\n"), 0o644)
}
