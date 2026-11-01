// Copyright 2026 The garm-provider-agent37 contributors
// SPDX-License-Identifier: Apache-2.0

package tailscale

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

type Config struct {
	Enabled         bool     `toml:"enabled" json:"enabled"`
	OAuthClientID   string   `toml:"oauth_client_id" json:"-"`
	OAuthSecretFile string   `toml:"oauth_secret_file" json:"-"`
	Tag             string   `toml:"tag" json:"tag"`
	GatewayDNS      string   `toml:"gateway_dns" json:"gateway_dns"`
	PrivateAliases  []string `toml:"private_aliases" json:"private_aliases"`
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var tagPattern = regexp.MustCompile(`^tag:[a-zA-Z][a-zA-Z0-9-]{0,62}$`)

func hostname(value string) bool {
	if len(value) > 253 || !strings.Contains(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !dnsLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.OAuthClientID == "" || len(c.OAuthClientID) > 256 || strings.ContainsAny(c.OAuthClientID, " \t\r\n\x00") || !filepath.IsAbs(c.OAuthSecretFile) {
		return fmt.Errorf("Validate: Tailscale requires oauth_client_id and an absolute oauth_secret_file")
	}
	if !tagPattern.MatchString(c.Tag) {
		return fmt.Errorf("Validate: Tailscale requires one worker tag")
	}
	if !hostname(c.GatewayDNS) || !strings.HasSuffix(c.GatewayDNS, ".ts.net") {
		return fmt.Errorf("Validate: Tailscale gateway_dns must be a lowercase MagicDNS hostname without a trailing dot")
	}
	if len(c.PrivateAliases) > 16 {
		return fmt.Errorf("Validate: at most 16 Tailscale private_aliases are supported")
	}
	seen := map[string]bool{c.GatewayDNS: true}
	for _, alias := range c.PrivateAliases {
		if !hostname(alias) || seen[alias] {
			return fmt.Errorf("Validate: Tailscale private_aliases must be distinct lowercase hostnames")
		}
		seen[alias] = true
	}
	return nil
}
