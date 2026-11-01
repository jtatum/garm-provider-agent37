// Copyright 2026 The garm-provider-agent37 contributors
// SPDX-License-Identifier: Apache-2.0

package tailscale

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed guest.py
var guest string

const VerifyCommand = "python3 /root/garm-network/network.py verify"
const LogoutCommand = "timeout 3s /usr/local/bin/tailscale --socket=/root/garm-network/tailscaled.sock logout >/dev/null 2>&1"

func Command(cfg Config, key, name string) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", fmt.Errorf("Command: %w", err)
	}
	if !cfg.Enabled || !token(key) || !strings.HasPrefix(key, "tskey-auth-") || !dnsLabel.MatchString(name) {
		return "", fmt.Errorf("Command: network configuration, auth key and hostname are required")
	}
	data, err := json.Marshal(map[string]any{"auth_key": key, "hostname": name, "gateway_dns": cfg.GatewayDNS, "private_aliases": cfg.PrivateAliases})
	if err != nil {
		return "", fmt.Errorf("Command: cannot encode network configuration")
	}
	code := "import base64,json\nsource=base64.b64decode('" + base64.StdEncoding.EncodeToString([]byte(guest)) + "').decode()\nnamespace={'__name__':'garm_network'}\nexec(source,namespace)\ntry:\n namespace['setup'](json.loads(base64.b64decode('" + base64.StdEncoding.EncodeToString(data) + "')),source)\nexcept Exception:\n raise SystemExit('worker network setup failed; replace worker')\n"
	return "python3 - <<'GARM_NETWORK'\n" + code + "GARM_NETWORK\n", nil
}
