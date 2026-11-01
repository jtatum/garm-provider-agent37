// Copyright 2026 The garm-provider-agent37 contributors
// SPDX-License-Identifier: Apache-2.0

package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const apiURL = "https://api.tailscale.com"
const KeyExpirySeconds = 600

type Client struct {
	cfg    Config
	origin string
	http   *http.Client
}

func New(cfg Config) *Client {
	return &Client{cfg: cfg, origin: apiURL, http: &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func token(value string) bool {
	return len(value) > 0 && len(value) <= 16*1024 && !strings.ContainsAny(value, " \t\r\n\x00")
}

func (c *Client) Mint(ctx context.Context) (string, error) {
	raw, err := os.ReadFile(c.cfg.OAuthSecretFile)
	if err != nil {
		return "", fmt.Errorf("Mint: cannot read Tailscale OAuth secret file")
	}
	secret := strings.TrimSpace(string(raw))
	if !token(secret) {
		return "", fmt.Errorf("Mint: invalid Tailscale OAuth secret file")
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {c.cfg.OAuthClientID}, "client_secret": {secret}, "scope": {"auth_keys"}}
	var access struct {
		Token string `json:"access_token"`
		Type  string `json:"token_type"`
	}
	if err := c.post(ctx, "/api/v2/oauth/token", "application/x-www-form-urlencoded", "", []byte(form.Encode()), &access); err != nil {
		return "", fmt.Errorf("Mint: OAuth exchange: %w", err)
	}
	if !token(access.Token) || !strings.EqualFold(access.Type, "bearer") {
		return "", fmt.Errorf("Mint: invalid OAuth response")
	}
	body, _ := json.Marshal(map[string]any{
		"capabilities": map[string]any{"devices": map[string]any{"create": map[string]any{
			"reusable": false, "ephemeral": true, "preauthorized": true, "tags": []string{c.cfg.Tag},
		}}},
		"expirySeconds": KeyExpirySeconds, "description": "GARM Agent37 one-job worker",
	})
	var result struct {
		Key string `json:"key"`
	}
	if err := c.post(ctx, "/api/v2/tailnet/-/keys", "application/json", access.Token, body, &result); err != nil {
		return "", fmt.Errorf("Mint: auth key creation: %w", err)
	}
	if !token(result.Key) || !strings.HasPrefix(result.Key, "tskey-auth-") {
		return "", fmt.Errorf("Mint: invalid auth key response")
	}
	return result.Key, nil
}

func (c *Client) post(ctx context.Context, path, contentType, bearer string, body []byte, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.origin+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post: invalid request")
	}
	req.Header.Set("Content-Type", contentType)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("post: Tailscale API transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("post: Tailscale API returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil || len(data) > 64*1024 || json.Unmarshal(data, result) != nil {
		return fmt.Errorf("post: invalid Tailscale API response")
	}
	return nil
}
