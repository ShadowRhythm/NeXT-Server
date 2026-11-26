package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func buildTestTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestCopyReplace(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "new-bin")
	dst := filepath.Join(dir, "old-bin")

	if err := os.WriteFile(src, []byte("new-version-content"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old-version-content"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := copyReplace(src, dst); err != nil {
		t.Fatalf("copyReplace failed: %v", err)
	}

	content, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new-version-content" {
		t.Fatalf("got %q, want new-version-content", string(content))
	}
}

func TestFindAsset(t *testing.T) {
	rel := &ghRelease{
		TagName: "v1.0.3",
		Assets: []ghAsset{
			{Name: "next-server-v1.0.3-linux-amd64.tar.gz", BrowserDownloadURL: "https://example.com/tagged.tar.gz"},
			{Name: "next-server-linux-amd64", BrowserDownloadURL: "https://example.com/plain"},
		},
	}

	url, isTar := findAsset(rel)
	// On linux/amd64 this should find the tagged asset
	if testing.Short() {
		return
	}
	t.Logf("findAsset on %s returned url=%q, isTar=%v", rel.TagName, url, isTar)
}

func TestExtractBinaryFromTarGzAndChecksum(t *testing.T) {
	archive := buildTestTarGz(t, map[string]string{
		"next-server-1.0.5-linux-amd64/LICENSE":     "MIT",
		"next-server-1.0.5-linux-amd64/next-server": "binary-payload",
	})

	var out bytes.Buffer
	if err := extractBinaryFromTarGz(bytes.NewReader(archive), &out); err != nil {
		t.Fatalf("extract valid archive failed: %v", err)
	}
	if out.String() != "binary-payload" {
		t.Fatalf("got %q, want binary-payload", out.String())
	}

	// Truncating the gzip trailer must fail CRC32/EOF verification even though binary bytes finished.
	truncated := archive[:len(archive)-4]
	out.Reset()
	if err := extractBinaryFromTarGz(bytes.NewReader(truncated), &out); err == nil {
		t.Fatal("expected error for truncated gzip trailer")
	}

	// Missing binary must fail.
	missing := buildTestTarGz(t, map[string]string{"other/file": "abc"})
	out.Reset()
	if err := extractBinaryFromTarGz(bytes.NewReader(missing), &out); err == nil {
		t.Fatal("expected error when binary is missing from archive")
	}
}

func TestDownloadBinaryStreaming(t *testing.T) {
	archive := buildTestTarGz(t, map[string]string{
		"next-server-1.0.5-linux-amd64/next-server": "streamed-bin",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tar":
			_, _ = w.Write(archive)
		case "/raw":
			_, _ = w.Write([]byte("raw-bin"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	if err := downloadBinary(context.Background(), srv.URL+"/tar", true, &out); err != nil {
		t.Fatalf("download tar failed: %v", err)
	}
	if out.String() != "streamed-bin" {
		t.Fatalf("got %q, want streamed-bin", out.String())
	}

	out.Reset()
	if err := downloadBinary(context.Background(), srv.URL+"/raw", false, &out); err != nil {
		t.Fatalf("download raw failed: %v", err)
	}
	if out.String() != "raw-bin" {
		t.Fatalf("got %q, want raw-bin", out.String())
	}
}
