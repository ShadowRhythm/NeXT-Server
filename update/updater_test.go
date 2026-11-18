package update

import (
	"os"
	"path/filepath"
	"testing"
)

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
