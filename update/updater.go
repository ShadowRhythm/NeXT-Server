// Package update provides self-update functionality for next-server.
// When the panel signals an upgrade, the server downloads the latest release
// binary from GitHub, replaces itself, and exits so the process manager
// (e.g. systemd) can restart it with the new binary.
package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const updateTimeout = 90 * time.Second

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Do downloads the latest release for the current platform from the given
// GitHub repo (e.g. "ShadowRhythm/NeXT-Server"), atomically replaces the
// running binary, then returns. The caller should exit so the process manager
// restarts it with the new binary.
func Do(ctx context.Context, repo string) error {
	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()

	rel, err := fetchLatestRelease(ctx, repo)
	if err != nil {
		return fmt.Errorf("fetch release info: %w", err)
	}

	downloadURL, isTar := findAsset(rel)
	if downloadURL == "" {
		return fmt.Errorf("no asset for %s/%s in release %s", runtime.GOOS, runtime.GOARCH, rel.TagName)
	}

	// Download to a temp file
	dlTmp, err := os.CreateTemp("", "next-server-dl-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	dlTmpName := dlTmp.Name()
	defer os.Remove(dlTmpName)

	if err := downloadTo(ctx, downloadURL, dlTmp); err != nil {
		dlTmp.Close()
		return fmt.Errorf("download: %w", err)
	}
	if _, err := dlTmp.Seek(0, io.SeekStart); err != nil {
		dlTmp.Close()
		return err
	}

	// Extract binary to another temp file
	binTmp, err := os.CreateTemp("", "next-server-bin-*")
	if err != nil {
		dlTmp.Close()
		return fmt.Errorf("create binary temp: %w", err)
	}
	binTmpName := binTmp.Name()
	defer os.Remove(binTmpName)

	if isTar {
		err = extractBinaryFromTarGz(dlTmp, binTmp)
	} else {
		_, err = io.Copy(binTmp, dlTmp)
	}
	dlTmp.Close()
	if err != nil {
		binTmp.Close()
		return fmt.Errorf("extract binary: %w", err)
	}
	if err := binTmp.Chmod(0o755); err != nil {
		binTmp.Close()
		return fmt.Errorf("chmod binary: %w", err)
	}
	binTmp.Close()

	// Resolve the current executable path (follow symlinks)
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("eval symlinks: %w", err)
	}

	// Atomic replace: try rename first (works on same filesystem), then copy
	if err := os.Rename(binTmpName, exePath); err != nil {
		if err2 := copyReplace(binTmpName, exePath); err2 != nil {
			return fmt.Errorf("replace binary (rename: %v; copy: %v)", err, err2)
		}
	}

	return nil
}

func fetchLatestRelease(ctx context.Context, repo string) (*ghRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API: HTTP %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// findAsset returns the download URL and whether it is a .tar.gz archive.
// It tries "<name>-<tag>-<os>-<arch>.tar.gz", then "<name>-<os>-<arch>".
func findAsset(rel *ghRelease) (url string, isTar bool) {
	tagged := fmt.Sprintf("next-server-%s-%s-%s.tar.gz", rel.TagName, runtime.GOOS, runtime.GOARCH)
	plain := fmt.Sprintf("next-server-%s-%s", runtime.GOOS, runtime.GOARCH)
	for _, a := range rel.Assets {
		if a.Name == tagged {
			return a.BrowserDownloadURL, true
		}
	}
	for _, a := range rel.Assets {
		if a.Name == plain {
			return a.BrowserDownloadURL, false
		}
	}
	return "", false
}

func downloadTo(ctx context.Context, url string, dst io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	_, err = io.Copy(dst, resp.Body)
	return err
}

func extractBinaryFromTarGz(r io.Reader, dst io.Writer) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("binary 'next-server' not found in archive")
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		base := filepath.Base(hdr.Name)
		if base == "next-server" || strings.HasPrefix(base, "next-server") && !strings.Contains(base, ".") {
			_, err = io.Copy(dst, tr)
			return err
		}
	}
}

func copyReplace(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
