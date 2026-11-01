// Copyright 2026 The garm-provider-agent37 contributors
// SPDX-License-Identifier: Apache-2.0

package tailscale

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "oauth-secret")
	if err := os.WriteFile(path, []byte("oauth-private-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{Enabled: true, OAuthClientID: "client-id", OAuthSecretFile: path, Tag: "tag:ci-worker", GatewayDNS: "ci.example.ts.net", PrivateAliases: []string{"git.internal.example"}}
}

func TestMintScopedEphemeralKey(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost {
			t.Error("unexpected method")
		}
		switch r.URL.Path {
		case "/api/v2/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("client_id") != "client-id" || r.Form.Get("client_secret") != "oauth-private-secret" || r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("scope") != "auth_keys" {
				t.Error("incorrect OAuth scope or credentials")
			}
			fmt.Fprint(w, `{"access_token":"access-private-token","token_type":"Bearer"}`)
		case "/api/v2/tailnet/-/keys":
			if r.Header.Get("Authorization") != "Bearer access-private-token" {
				t.Error("missing access token")
			}
			var body struct {
				Expiry       int `json:"expirySeconds"`
				Capabilities struct {
					Devices struct {
						Create struct {
							Reusable, Ephemeral, Preauthorized bool
							Tags                               []string
						}
					}
				}
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			create := body.Capabilities.Devices.Create
			if body.Expiry != 600 || create.Reusable || !create.Ephemeral || !create.Preauthorized || len(create.Tags) != 1 || create.Tags[0] != "tag:ci-worker" {
				t.Error("key is not single-use, ephemeral, preauthorized and narrowly tagged")
			}
			fmt.Fprint(w, `{"key":"tskey-auth-private-worker"}`)
		default:
			t.Error("unexpected endpoint")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := New(testConfig(t))
	c.origin = server.URL
	key, err := c.Mint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if key != "tskey-auth-private-worker" || calls != 2 {
		t.Fatal("unexpected enrollment flow")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestErrorsNeverIncludeCredentialOrResponse(t *testing.T) {
	for _, failure := range []string{"oauth", "key", "redirect", "malformed", "transport"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if failure == "redirect" {
					w.Header().Set("Location", "/oauth-private-secret")
					w.WriteHeader(307)
					return
				}
				if failure == "malformed" {
					fmt.Fprint(w, "oauth-private-secret")
					return
				}
				if r.URL.Path == "/api/v2/oauth/token" && failure != "oauth" {
					fmt.Fprint(w, `{"access_token":"access-private-token","token_type":"Bearer"}`)
					return
				}
				w.WriteHeader(403)
				fmt.Fprint(w, "oauth-private-secret access-private-token tskey-auth-private-worker")
			}))
			defer server.Close()
			c := New(testConfig(t))
			c.origin = server.URL
			if failure == "transport" {
				c.http.Transport = roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("oauth-private-secret") })
			}
			_, err := c.Mint(context.Background())
			if err == nil {
				t.Fatal("expected rejection")
			}
			for _, secret := range []string{"oauth-private-secret", "access-private-token", "tskey-auth-private-worker"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("credential appeared in error")
				}
			}
			if failure == "redirect" && calls != 1 {
				t.Fatal("followed credential-bearing redirect")
			}
		})
	}
}

func TestValidateAndCommand(t *testing.T) {
	cfg := testConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, alter := range []func(*Config){
		func(c *Config) { c.OAuthSecretFile = "relative" },
		func(c *Config) { c.OAuthClientID = "bad\nclient" },
		func(c *Config) { c.Tag = "tag:worker,tag:admin" },
		func(c *Config) { c.GatewayDNS = "https://gateway.example.ts.net" },
		func(c *Config) { c.GatewayDNS = "gateway.example.ts.net\nserver=evil" },
		func(c *Config) { c.PrivateAliases = []string{"*.example"} },
		func(c *Config) { c.PrivateAliases = []string{"dup.example", "dup.example"} },
	} {
		bad := cfg
		alter(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatal("unsafe network configuration accepted")
		}
	}
	cfg.Enabled = false
	cfg.OAuthSecretFile = ""
	if err := cfg.Validate(); err != nil {
		t.Fatal("disabled network requires credentials")
	}
	cfg = testConfig(t)
	command, err := Command(cfg, "tskey-auth-private-worker", "garm-worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"oauth-private-secret", cfg.OAuthSecretFile, "tskey-auth-private-worker", cfg.OAuthClientID} {
		if strings.Contains(command, secret) {
			t.Fatal("raw credential or controller-only setting appears in guest command")
		}
	}
	parts := regexp.MustCompile(`b64decode\('([A-Za-z0-9+/=]+)'\)`).FindAllStringSubmatch(command, -1)
	if len(parts) != 2 {
		t.Fatal("unexpected payload envelope")
	}
	payload, err := base64.StdEncoding.DecodeString(parts[1][1])
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"oauth-private-secret", cfg.OAuthSecretFile, cfg.OAuthClientID} {
		if strings.Contains(string(payload), secret) {
			t.Fatal("controller-only credential entered guest payload")
		}
	}
	if len(command) > 96*1024 {
		t.Fatal("network command exceeds exec envelope")
	}
}

func TestGuestRuntime(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required")
	}
	cmd := exec.Command(python, "test_guest.py")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("guest contract failed: %v\n%s", err, output)
	}
}
