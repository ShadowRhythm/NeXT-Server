package hysteria2

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/The-NeXT-Project/NeXT-Server/option"

	C "github.com/sagernet/sing-box/constant"
	boxOption "github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badoption"
)

func TestMasqueradeJSONDecode(t *testing.T) {
	testCases := []struct {
		name        string
		raw         string
		wantType    string
		wantURL     string
		wantRewrite bool
		wantDir     string
	}{
		{
			name:     "proxy shorthand string",
			raw:      `{"servers":[{"url":"https://panel.example.com","key":"k","masquerade":"http://127.0.0.1:80"}]}`,
			wantType: C.Hysterai2MasqueradeTypeProxy,
			wantURL:  "http://127.0.0.1:80",
		},
		{
			name:        "proxy object",
			raw:         `{"servers":[{"url":"https://panel.example.com","key":"k","masquerade":{"type":"proxy","url":"https://www.bing.com","rewrite_host":true}}]}`,
			wantType:    C.Hysterai2MasqueradeTypeProxy,
			wantURL:     "https://www.bing.com",
			wantRewrite: true,
		},
		{
			name:     "file object",
			raw:      `{"servers":[{"url":"https://panel.example.com","key":"k","masquerade":{"type":"file","directory":"/var/www/html"}}]}`,
			wantType: C.Hysterai2MasqueradeTypeFile,
			wantDir:  "/var/www/html",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var opts option.Options
			dec := json.NewDecoder(json.NewCommentFilter(bytes.NewReader([]byte(tc.raw))))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&opts); err != nil {
				t.Fatalf("decode failed: %v", err)
			}
			m := opts.Servers[0].Masquerade
			if m == nil {
				t.Fatal("expected non-nil masquerade")
			}
			if m.Type != tc.wantType {
				t.Fatalf("type = %q, want %q", m.Type, tc.wantType)
			}
			if m.ProxyOptions.URL != tc.wantURL {
				t.Fatalf("url = %q, want %q", m.ProxyOptions.URL, tc.wantURL)
			}
			if m.ProxyOptions.RewriteHost != tc.wantRewrite {
				t.Fatalf("rewrite_host = %v, want %v", m.ProxyOptions.RewriteHost, tc.wantRewrite)
			}
			if m.FileOptions.Directory != tc.wantDir {
				t.Fatalf("directory = %q, want %q", m.FileOptions.Directory, tc.wantDir)
			}
		})
	}
}

func TestBuildMasqueradeHandler(t *testing.T) {
	// String handler sets headers before WriteHeader.
	h, err := buildMasqueradeHandler(&boxOption.Hysteria2Masquerade{
		Type: C.Hysterai2MasqueradeTypeString,
		StringOptions: boxOption.Hysteria2MasqueradeString{
			StatusCode: http.StatusTeapot,
			Headers:    badoption.HTTPHeader{"X-Test": {"ok"}},
			Content:    "hello masquerade",
		},
	})
	if err != nil {
		t.Fatalf("string handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://terra.srss.shop/", nil))
	if rec.Code != http.StatusTeapot || rec.Header().Get("X-Test") != "ok" || rec.Body.String() != "hello masquerade" {
		t.Fatalf("unexpected string response: code=%d header=%q body=%q", rec.Code, rec.Header().Get("X-Test"), rec.Body.String())
	}

	// File handler serves static directory.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("static-site"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err = buildMasqueradeHandler(&boxOption.Hysteria2Masquerade{
		Type:        C.Hysterai2MasqueradeTypeFile,
		FileOptions: boxOption.Hysteria2MasqueradeFile{Directory: dir},
	})
	if err != nil {
		t.Fatalf("file handler: %v", err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://terra.srss.shop/test.txt", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "static-site" {
		t.Fatalf("unexpected file response: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// Proxy handler preserves Host when RewriteHost is false.
	var seenHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost = r.Host
		w.Header().Set("Server", "nginx/1.24.0")
		w.Write([]byte("nginx-home"))
	}))
	defer upstream.Close()

	h, err = buildMasqueradeHandler(&boxOption.Hysteria2Masquerade{
		Type:         C.Hysterai2MasqueradeTypeProxy,
		ProxyOptions: boxOption.Hysteria2MasqueradeProxy{URL: upstream.URL, RewriteHost: false},
	})
	if err != nil {
		t.Fatalf("proxy handler: %v", err)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://terra.srss.shop/", nil)
	h.ServeHTTP(rec, req)
	bodyBytes, _ := io.ReadAll(rec.Result().Body)
	if rec.Code != http.StatusOK || string(bodyBytes) != "nginx-home" || seenHost != "terra.srss.shop" || rec.Header().Get("Server") != "nginx/1.24.0" {
		t.Fatalf("unexpected proxy response: code=%d host=%q server=%q body=%q", rec.Code, seenHost, rec.Header().Get("Server"), string(bodyBytes))
	}

	// Invalid proxy URL rejected.
	if _, err := buildMasqueradeHandler(&boxOption.Hysteria2Masquerade{
		Type:         C.Hysterai2MasqueradeTypeProxy,
		ProxyOptions: boxOption.Hysteria2MasqueradeProxy{URL: "ftp://invalid"},
	}); err == nil {
		t.Fatal("expected error for invalid proxy URL scheme")
	}
}
