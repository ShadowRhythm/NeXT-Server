package main

import (
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
