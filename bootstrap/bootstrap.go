// Copyright 2026 The garm-provider-agent37 Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/cloudbase/garm-provider-common/params"
)

const EnvironmentKey = "GARM_BOOTSTRAP_CONFIG"
const ResumeCommand = "python3 /root/garm/launcher.py resume"

func Configuration(p params.BootstrapInstance) (string, error) {
	if p.Name == "" || p.InstanceToken == "" || strings.ContainsAny(p.InstanceToken, "\x00\r\n") {
		return "", fmt.Errorf("Configuration: runner name and instance token are required")
	}
	if p.OSType != params.Linux || p.OSArch != params.Amd64 {
		return "", fmt.Errorf("Configuration: only linux/amd64 is supported")
	}
	if p.JitConfigEnabled || p.GitHubRunnerGroup != "" || len(p.SSHKeys) != 0 || p.UserDataOptions.EnableBootDebug || len(p.UserDataOptions.ExtraPackages) != 0 {
		return "", fmt.Errorf("Configuration: JIT, runner groups, SSH key injection, boot debugging and extra packages are unsupported")
	}
	for _, value := range []string{p.MetadataURL, p.CallbackURL} {
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", fmt.Errorf("Configuration: invalid metadata or callback URL")
		}
	}
	cfg := struct {
		Name        string             `json:"name"`
		MetadataURL string             `json:"metadata_url"`
		CallbackURL string             `json:"callback_url"`
		Token       string             `json:"token"`
		CABundle    string             `json:"ca_bundle"`
		Proxy       params.ProxyConfig `json:"proxy"`
	}{p.Name, p.MetadataURL, p.CallbackURL, p.InstanceToken, string(p.CACertBundle), p.ProxyConfig}
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("Configuration: encode configuration: %w", err)
	}
	if len(data) > 96*1024 {
		return "", fmt.Errorf("Configuration: bootstrap payload too large")
	}
	return string(data), nil
}
