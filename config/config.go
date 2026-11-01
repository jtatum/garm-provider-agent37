// Copyright 2026 The garm-provider-agent37 contributors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/jtatum/garm-provider-agent37/client"
)

type Config struct {
	ProviderID    string `toml:"provider_id"`
	APIKeyFile    string `toml:"api_key_file"`
	StateDir      string `toml:"state_dir"`
	Region        string `toml:"region"`
	MaxInstances  int    `toml:"max_instances"`
	DefaultDiskGB int    `toml:"default_disk_gb"`
	CreateTimeout string `toml:"create_timeout"`
	PollInterval  string `toml:"poll_interval"`
}

type ExtraSpecs struct {
	DiskGB int `json:"disk_gb"`
}

var namespacePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)
var imagePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}@[1-9][0-9]*$`)

func Defaults() Config {
	return Config{Region: "us", MaxInstances: 2, DefaultDiskGB: 20, CreateTimeout: "4m", PollInterval: "1s"}
}

func Load(path string) (Config, error) {
	cfg := Defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("Load: read configuration: %w", err)
	}
	meta, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("Load: invalid TOML configuration")
	}
	if len(meta.Undecoded()) > 0 {
		return Config{}, fmt.Errorf("Load: unknown configuration field")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("Load: %w", err)
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if !namespacePattern.MatchString(c.ProviderID) {
		return fmt.Errorf("Validate: provider_id must be a stable 1–64 character namespace")
	}
	if !filepath.IsAbs(c.APIKeyFile) || !filepath.IsAbs(c.StateDir) || c.StateDir == string(filepath.Separator) {
		return fmt.Errorf("Validate: api_key_file and state_dir must be absolute paths")
	}
	if c.Region != "us" && c.Region != "eu" && c.Region != "ap-south" {
		return fmt.Errorf("Validate: unsupported region")
	}
	if c.MaxInstances < 1 || c.MaxInstances > 200 {
		return fmt.Errorf("Validate: max_instances must be between 1 and 200")
	}
	if c.DefaultDiskGB < 2 || c.DefaultDiskGB > 20 {
		return fmt.Errorf("Validate: default_disk_gb must be between 2 and 20")
	}
	deadline, err := time.ParseDuration(c.CreateTimeout)
	if err != nil || deadline < time.Second || deadline > 15*time.Minute {
		return fmt.Errorf("Validate: create_timeout must be between 1s and 15m")
	}
	poll, err := time.ParseDuration(c.PollInterval)
	if err != nil || poll < 100*time.Millisecond || poll > 30*time.Second {
		return fmt.Errorf("Validate: poll_interval must be between 100ms and 30s")
	}
	return nil
}

func (c Config) ReadKey() (string, error) {
	data, err := os.ReadFile(c.APIKeyFile)
	if err != nil {
		return "", fmt.Errorf("ReadKey: cannot read API key file")
	}
	key := strings.TrimSpace(string(data))
	if key == "" || len(key) > 16*1024 || strings.ContainsAny(key, "\r\n\x00") {
		return "", fmt.Errorf("ReadKey: API key file must contain one nonempty token")
	}
	return key, nil
}

func ParseExtraSpecs(raw string) (ExtraSpecs, error) {
	if strings.TrimSpace(raw) == "" {
		return ExtraSpecs{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
		return ExtraSpecs{}, fmt.Errorf("ParseExtraSpecs: expected a JSON object")
	}
	for field := range fields {
		switch field {
		case "disk_gb", "extra_context", "runner_install_template":
		default:
			return ExtraSpecs{}, fmt.Errorf("ParseExtraSpecs: unsupported extra spec")
		}
	}
	var specs ExtraSpecs
	if err := json.Unmarshal([]byte(raw), &specs); err != nil || specs.DiskGB < 0 {
		return ExtraSpecs{}, fmt.Errorf("ParseExtraSpecs: invalid disk_gb")
	}
	return specs, nil
}

func (c Config) Resources(image, flavor, extraSpecs string) (client.Resources, error) {
	if !imagePattern.MatchString(image) || strings.HasPrefix(image, "agent37-") {
		return client.Resources{}, fmt.Errorf("Resources: image must pin a workspace template as name@revision")
	}
	spec, err := ParseExtraSpecs(extraSpecs)
	if err != nil {
		return client.Resources{}, fmt.Errorf("Resources: %w", err)
	}
	var resources client.Resources
	switch flavor {
	case "small":
		resources = client.Resources{CPU: 2, Memory: 4, Disk: min(c.DefaultDiskGB, 12)}
	case "default":
		resources = client.Resources{CPU: 4, Memory: 8, Disk: c.DefaultDiskGB}
	default:
		return client.Resources{}, fmt.Errorf("Resources: flavor must be small or default")
	}
	if spec.DiskGB != 0 {
		resources.Disk = spec.DiskGB
	}
	maxDisk := 20
	if resources.CPU == 2 {
		maxDisk = 12
	}
	if resources.Disk < 2 || resources.Disk > maxDisk {
		return client.Resources{}, fmt.Errorf("Resources: disk_gb is outside the flavor's supported range")
	}
	return resources, nil
}

const JSONSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,"required":["provider_id","api_key_file","state_dir"],"properties":{"provider_id":{"type":"string","pattern":"^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$"},"api_key_file":{"type":"string"},"state_dir":{"type":"string"},"region":{"type":"string","enum":["us","eu","ap-south"],"default":"us"},"max_instances":{"type":"integer","minimum":1,"maximum":200,"default":2},"default_disk_gb":{"type":"integer","minimum":2,"maximum":20,"default":20},"create_timeout":{"type":"string","default":"4m"},"poll_interval":{"type":"string","default":"1s"}}}`

const ExtraSpecsJSONSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,"properties":{"disk_gb":{"type":"integer","minimum":2,"maximum":20},"extra_context":{"type":"object","additionalProperties":{"type":"string"}},"runner_install_template":{"type":"string"}}}`
