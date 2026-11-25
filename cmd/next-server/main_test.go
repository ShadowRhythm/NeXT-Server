package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureAbuseInConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")

	initial := `{
  "log": { "level": "info" },
  "servers": [
    {
      "url": "https://panel.example.com",
      "key": "test-key"
    }
  ]
}`
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	ensureAbuseInConfig(configPath)

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	expectedKeys := []string{
		`"abuse"`,
		`"p2p"`,
		`"rule_id": 11`,
		`"port_scan"`,
		`"rule_id": 12`,
		`"flood"`,
		`"rule_id": 13`,
		`"brute_force"`,
		`"rule_id": 14`,
		`"blocked_ports"`,
		`"rule_id": 15`,
		`"exploits"`,
		`"rule_id": 16`,
		`"mining"`,
		`"rule_id": 17`,
	}
	for _, k := range expectedKeys {
		if !strings.Contains(content, k) {
			t.Fatalf("expected config to contain %s, got:\n%s", k, content)
		}
	}

	// Run again to ensure idempotency and no corruption
	ensureAbuseInConfig(configPath)
	dataAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(dataAfter) != content {
		t.Fatalf("expected idempotent output, got:\n%s\nvs\n%s", string(dataAfter), content)
	}
}

func TestEnsureMasqueradeInConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")

	initial := `{
  "log": { "level": "info" },
  "servers": [
    {
      "url": "https://xxx.org",
      "key": "xxxxx",
      "listen": "127.0.0.1",
      "tls": { "enabled": false }
    },
    {
      "url": "https://xxxxx.org",
      "key": "xxxxxx",
      "listen": "::",
      "tls": {
        "certificate_path": "/etc/nginx/ssl/xxxx.shop/fullchain.cer",
        "key_path": "/etc/nginx/ssl/xxxx.shop/xxxx.shop.key"
      }
    },
    {
      "url": "https://optout.org",
      "key": "yyyy",
      "listen": "::",
      "masquerade": null
    }
  ]
}`
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	ensureAbuseInConfig(configPath)

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	var parsed struct {
		Servers []map[string]any `json:"servers"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal updated config: %v", err)
	}
	if _, ok := parsed.Servers[0]["masquerade"]; ok {
		t.Fatalf("expected server[0] with tls.enabled=false not to get masquerade, got %v", parsed.Servers[0]["masquerade"])
	}
	if got := parsed.Servers[1]["masquerade"]; got != "http://127.0.0.1:80" {
		t.Fatalf("server[1] masquerade = %v, want http://127.0.0.1:80", got)
	}
	if v, ok := parsed.Servers[2]["masquerade"]; !ok || v != nil {
		t.Fatalf("expected server[2] explicit null masquerade preserved, got ok=%v v=%v", ok, v)
	}
}
