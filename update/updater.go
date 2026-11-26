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

	// Resolve the current executable path (follow symlinks)
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("eval symlinks: %w", err)
	}
	exeDir := filepath.Dir(exePath)

	// Stream download + extraction directly to a temp file in the same directory
	// as exePath so os.Rename is atomic on the same filesystem without touching /tmp.
	binTmp, err := os.CreateTemp(exeDir, ".next-server-bin-*")
	if err != nil {
		// Fallback to default temp dir if exeDir is not writable for temp files
		binTmp, err = os.CreateTemp("", "next-server-bin-*")
		if err != nil {
			return fmt.Errorf("create binary temp: %w", err)
		}
	}
	binTmpName := binTmp.Name()
	defer os.Remove(binTmpName)

	if err := downloadBinary(ctx, downloadURL, isTar, binTmp); err != nil {
		binTmp.Close()
		return err
	}
	if err := binTmp.Chmod(0o755); err != nil {
		binTmp.Close()
		return fmt.Errorf("chmod binary: %w", err)
	}
	if err := binTmp.Close(); err != nil {
		return fmt.Errorf("close binary temp: %w", err)
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

func downloadBinary(ctx context.Context, url string, isTar bool, dst io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	if isTar {
		if err := extractBinaryFromTarGz(resp.Body, dst); err != nil {
			return fmt.Errorf("extract binary: %w", err)
		}
		return nil
	}
	n, err := io.Copy(dst, resp.Body)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return fmt.Errorf("download: incomplete body (%d of %d bytes)", n, resp.ContentLength)
	}
	return nil
}

func extractBinaryFromTarGz(r io.Reader, dst io.Writer) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		base := filepath.Base(hdr.Name)
		if base == "next-server" || strings.HasPrefix(base, "next-server") && !strings.Contains(base, ".") {
			if _, err = io.Copy(dst, tr); err != nil {
				return err
			}
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("binary 'next-server' not found in archive")
	}
	// Drain trailing tar padding so gzip verifies the full CRC32 and length trailer.
	_, err = io.Copy(io.Discard, gz)
	return err
}

func copyReplace(src, dst string) error {
	dstDir := filepath.Dir(dst)
	// 1. Try to create a temp file in dstDir, copy src to it, and rename it over dst.
	// Since temp file is in dstDir, rename will be on the same filesystem.
	// In Linux, rename over a running binary succeeds (unlinking the old inode without ETXTBSY).
	tmp, err := os.CreateTemp(dstDir, ".next-server-copy-*")
	if err == nil {
		tmpName := tmp.Name()
		defer os.Remove(tmpName)

		in, err := os.Open(src)
		if err != nil {
			tmp.Close()
			return err
		}
		defer in.Close()

		if _, err := io.Copy(tmp, in); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Chmod(0o755); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}

		if err := os.Rename(tmpName, dst); err == nil {
			return nil
		}
	}

	// 2. If creating temp in dstDir or rename fails, remove dst first (unlink avoids ETXTBSY on running binary)
	// Backup to dst.old first so we don't lose the binary if copy fails
	backup := dst + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(dst, backup); err == nil {
		defer os.Remove(backup)
	} else {
		_ = os.Remove(dst)
	}

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
