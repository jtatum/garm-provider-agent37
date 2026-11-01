// Copyright 2026 The garm-provider-agent37 contributors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResources(t *testing.T) {
	cfg := Defaults()
	for _, tc := range []struct {
		image, flavor, extra string
		disk                 int
		valid                bool
	}{
		{"runner-template@2", "default", "", 20, true},
		{"runner-template@2", "small", "", 12, true},
		{"runner-template@2", "small", `{"disk_gb":8,"extra_context":{"agent37_container_network":"host"},"runner_install_template":"eA=="}`, 8, true},
		{"runner-template", "default", "", 0, false},
		{"agent37-ubuntu@2", "default", "", 0, false},
		{"runner-template@0", "default", "", 0, false},
		{"runner-template@2", "huge", "", 0, false},
		{"runner-template@2", "small", `{"disk_gb":20}`, 0, false},
		{"runner-template@2", "default", `{"disk_gb":1}`, 0, false},
		{"runner-template@2", "default", `{"disk_gb":-1}`, 0, false},
		{"runner-template@2", "default", `{"typo":20}`, 0, false},
		{"runner-template@2", "default", `null`, 0, false},
	} {
		t.Run(tc.image+tc.flavor+tc.extra, func(t *testing.T) {
			r, err := cfg.Resources(tc.image, tc.flavor, tc.extra)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
			if err == nil && r.Disk != tc.disk {
				t.Fatalf("disk=%d, want=%d", r.Disk, tc.disk)
			}
		})
	}
}

func TestLoadAndSecretRedaction(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte("secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	content := "provider_id = 'lab'\napi_key_file = '" + key + "'\nstate_dir = '" + dir + "'\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxInstances != 2 || cfg.Region != "us" {
		t.Fatalf("missing defaults: %+v", cfg)
	}
	got, err := cfg.ReadKey()
	if err != nil || got != "secret-value" {
		t.Fatalf("key read failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(content+"api_key = 'secret-value'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Load(path)
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("unknown key not safely rejected: %v", err)
	}
	if err := os.WriteFile(path, []byte(content+"oops = secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Load(path)
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("TOML not safely rejected: %v", err)
	}
}
