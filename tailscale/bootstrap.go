// Copyright 2026 The garm-provider-agent37 contributors
// SPDX-License-Identifier: Apache-2.0

package tailscale

import (
	"encoding/json"
	"fmt"
	"strings"
)

const EnvironmentKey = "GARM_NETWORK_CONFIG"
const LogoutCommand = "timeout 3s /usr/local/bin/tailscale --socket=/root/garm-network/tailscaled.sock logout >/dev/null 2>&1"

func Configuration(cfg Config, key, name string) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", fmt.Errorf("Configuration: %w", err)
	}
	if !cfg.Enabled || !token(key) || !strings.HasPrefix(key, "tskey-auth-") || !dnsLabel.MatchString(name) {
		return "", fmt.Errorf("Configuration: network configuration, auth key and hostname are required")
	}
	data, err := json.Marshal(map[string]any{"auth_key": key, "hostname": name, "gateway_dns": cfg.GatewayDNS, "private_aliases": cfg.PrivateAliases})
	if err != nil {
		return "", fmt.Errorf("Configuration: cannot encode network configuration")
	}
	if len(data) > 96*1024 {
		return "", fmt.Errorf("Configuration: network payload too large")
	}
	return string(data), nil
}
