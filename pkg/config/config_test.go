package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndValidate(t *testing.T) {
	cfgJSON := `{
		"gateway": {
			"listen": ":8092",
			"data_dir": "./data",
			"hostname": "test.local"
		},
		"agents": {
			"test-agent": {
				"display_name": "Test Agent",
				"public_key": "x25519:dGVzdA=="
			}
		},
		"channels": {
			"test-channel": {
				"type": "webhook",
				"route_to": "test-agent",
				"trust": "trusted"
			}
		},
		"scheduler": {
			"jobs": {
				"daily-check": {
					"schedule": "0 9 * * *",
					"route_to": "test-agent",
					"prompt": "Do the thing."
				}
			}
		}
	}`

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(cfgJSON), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Gateway.Listen != ":8092" {
		t.Errorf("expected :8092, got %s", cfg.Gateway.Listen)
	}
	if len(cfg.Agents) != 1 {
		t.Errorf("expected 1 agent, got %d", len(cfg.Agents))
	}
	if len(cfg.Channels) != 1 {
		t.Errorf("expected 1 channel, got %d", len(cfg.Channels))
	}
	if len(cfg.Scheduler.Jobs) != 1 {
		t.Errorf("expected 1 job, got %d", len(cfg.Scheduler.Jobs))
	}
}

func TestValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{
			"missing listen",
			`{"gateway":{"data_dir":"./data"},"agents":{},"channels":{},"scheduler":{"jobs":{}}}`,
		},
		{
			"missing data_dir",
			`{"gateway":{"listen":":8080"},"agents":{},"channels":{},"scheduler":{"jobs":{}}}`,
		},
		{
			"agent missing public key",
			`{"gateway":{"listen":":8080","data_dir":"./data"},"agents":{"a":{"display_name":"A"}},"channels":{},"scheduler":{"jobs":{}}}`,
		},
		{
			"channel missing route_to",
			`{"gateway":{"listen":":8080","data_dir":"./data"},"agents":{"a":{"public_key":"x25519:dGVzdA=="}},"channels":{"c":{"type":"webhook","trust":"trusted"}},"scheduler":{"jobs":{}}}`,
		},
		{
			"channel routes to unknown agent",
			`{"gateway":{"listen":":8080","data_dir":"./data"},"agents":{"a":{"public_key":"x25519:dGVzdA=="}},"channels":{"c":{"type":"webhook","route_to":"nonexistent","trust":"trusted"}},"scheduler":{"jobs":{}}}`,
		},
		{
			"channel missing trust",
			`{"gateway":{"listen":":8080","data_dir":"./data"},"agents":{"a":{"public_key":"x25519:dGVzdA=="}},"channels":{"c":{"type":"webhook","route_to":"a"}},"scheduler":{"jobs":{}}}`,
		},
		{
			"scheduler routes to unknown agent",
			`{"gateway":{"listen":":8080","data_dir":"./data"},"agents":{"a":{"public_key":"x25519:dGVzdA=="}},"channels":{},"scheduler":{"jobs":{"j":{"schedule":"0 9 * * *","route_to":"nonexistent","prompt":"hi"}}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			os.WriteFile(path, []byte(tt.json), 0644)

			_, err := Load(path)
			if err == nil {
				t.Errorf("expected validation error for %s", tt.name)
			}
		})
	}
}
