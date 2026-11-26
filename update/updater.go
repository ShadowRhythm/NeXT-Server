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
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	updateTimeout = 5 * time.Minute
	stallTimeout  = 30 * time.Second
)

// ErrInProgress is returned when another upgrade in the same process is
// already running (e.g. multiple server entries receiving hb.Upgrade together).
var ErrInProgress = errors.New("upgrade already in progress")

var updateMu sync.Mutex

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
	if !updateMu.TryLock() {
		return ErrInProgress
	}
	defer updateMu.Unlock()

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

	if err := downloadBinaryWithRetry(ctx, downloadURL, isTar, binTmp); err != nil {
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

type idleTimeoutConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleTimeoutConn) Read(p []byte) (int, error) {
	if c.timeout > 0 {
		if err := c.Conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
			return 0, err
		}
	}
	return c.Conn.Read(p)
}

func newUpdateClient(network string, readIdle time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, netw, addr string) (net.Conn, error) {
			if network != "" {
				netw = network
			}
			conn, err := dialer.DialContext(ctx, netw, addr)
			if err != nil {
				return nil, err
			}
			if readIdle > 0 {
				return &idleTimeoutConn{Conn: conn, timeout: readIdle}, nil
			}
			return conn, nil
		},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{Transport: transport}
}

func fetchLatestRelease(ctx context.Context, repo string) (*ghRelease, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	var apiErr error
	for _, network := range []string{"", "tcp4"} {
		if ctx.Err() != nil {
			break
		}
		rel, err := fetchLatestReleaseFromAPI(ctx, apiURL, network)
		if err == nil {
			return rel, nil
		}
		apiErr = err
	}
	// Fallback to GitHub web redirect when api.github.com is rate-limited (403/429).
	webURL := fmt.Sprintf("https://github.com/%s/releases/latest", repo)
	if rel, err := fetchLatestReleaseFromRedirect(ctx, repo, webURL, "tcp4"); err == nil {
		return rel, nil
	}
	return nil, apiErr
}

func fetchLatestReleaseFromAPI(ctx context.Context, apiURL, network string) (*ghRelease, error) {
	client := newUpdateClient(network, stallTimeout)
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
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

func fetchLatestReleaseFromRedirect(ctx context.Context, repo, webURL, network string) (*ghRelease, error) {
	client := newUpdateClient(network, stallTimeout)
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, webURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("GitHub releases redirect: HTTP %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "/releases/tag/") {
		return nil, fmt.Errorf("unexpected redirect location: %q", loc)
	}
	tag := path.Base(strings.TrimSuffix(loc, "/"))
	if tag == "" || tag == "latest" {
		return nil, fmt.Errorf("invalid release tag in %q", loc)
	}
	tagged := fmt.Sprintf("next-server-%s-%s-%s.tar.gz", tag, runtime.GOOS, runtime.GOARCH)
	return &ghRelease{
		TagName: tag,
		Assets: []ghAsset{{
			Name:               tagged,
			BrowserDownloadURL: fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, tagged),
		}},
	}, nil
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

func downloadBinaryWithRetry(ctx context.Context, url string, isTar bool, dst *os.File) error {
	var lastErr error
	// Try default dual-stack first, then fall back to IPv4 twice in case IPv6
	// to release-assets.githubusercontent.com is throttled/blackholed or CDN resets.
	for i, network := range []string{"", "tcp4", "tcp4"} {
		if ctx.Err() != nil {
			break
		}
		if i > 0 {
			if err := dst.Truncate(0); err != nil {
				return err
			}
			if _, err := dst.Seek(0, io.SeekStart); err != nil {
				return err
			}
		}
		client := newUpdateClient(network, stallTimeout)
		err := downloadBinaryWithClient(ctx, client, url, isTar, dst)
		client.CloseIdleConnections()
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return lastErr
}

func downloadBinary(ctx context.Context, url string, isTar bool, dst io.Writer) error {
	return downloadBinaryWithClient(ctx, http.DefaultClient, url, isTar, dst)
}

func downloadBinaryWithClient(ctx context.Context, client *http.Client, url string, isTar bool, dst io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	resp, err := client.Do(req)
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
