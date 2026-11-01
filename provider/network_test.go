// Copyright 2026 The garm-provider-agent37 contributors
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudbase/garm-provider-common/params"
	"github.com/jtatum/garm-provider-agent37/client"
	"github.com/jtatum/garm-provider-agent37/tailscale"
)

func networkProvider(t *testing.T, api *fakeAPI) (*Provider, *int) {
	t.Helper()
	p := testProvider(t, api)
	p.cfg.Tailscale = tailscale.Config{Enabled: true, OAuthClientID: "controller-client", OAuthSecretFile: "/private/controller-secret", Tag: "tag:worker", GatewayDNS: "ci.example.ts.net", PrivateAliases: []string{"git.internal.example"}}
	mints := new(int)
	p.mintNetworkKey = func(context.Context) (string, error) { *mints++; return "tskey-auth-worker-secret", nil }
	return p, mints
}

func TestNetworkPrecedesBootstrapAndIsNotReenrolled(t *testing.T) {
	api := newFake()
	p, mints := networkProvider(t, api)
	events := []string{}
	api.execFn = func(ctx context.Context, id, command, name string) (client.ExecResult, error) {
		if strings.HasPrefix(name, "garm-network-") && command != tailscale.VerifyCommand {
			stage, err := p.creationStage(ctx, "runner-network")
			if err != nil || stage != "networking" {
				t.Error("network intent not durable before enrollment")
			}
			events = append(events, "network")
		}
		if command == "bootstrap-command" {
			events = append(events, "bootstrap")
		}
		return client.ExecResult{}, nil
	}
	bp := testParams("runner-network")
	for range 2 {
		if _, err := p.CreateInstance(context.Background(), bp); err != nil {
			t.Fatal(err)
		}
	}
	if *mints != 1 || strings.Join(events, ",") != "network,bootstrap" {
		t.Fatalf("replayed enrollment or wrong order: %v", events)
	}
	data, err := os.ReadFile(filepath.Join(p.statePath, "creations.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"tskey-auth-worker-secret", "controller-secret", "controller-client"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("credential leaked to creation journal")
		}
	}
}

func seedNetworkStage(t *testing.T, p *Provider, api *fakeAPI, bp params.BootstrapInstance, stage string) client.Instance {
	t.Helper()
	req, err := p.request(bp)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = p.reserve(context.Background(), bp, req); err != nil {
		t.Fatal(err)
	}
	instance, err := api.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.record(context.Background(), bp.Name, instance.ID, stage); err != nil {
		t.Fatal(err)
	}
	return instance
}

func TestNetworkRecoveryVerifiesWithoutMinting(t *testing.T) {
	for _, stage := range []string{"networking", "networked"} {
		for _, healthy := range []bool{true, false} {
			t.Run(stage+map[bool]string{true: "-healthy", false: "-failed"}[healthy], func(t *testing.T) {
				api := newFake()
				p, mints := networkProvider(t, api)
				bp := testParams("recovery")
				seedNetworkStage(t, p, api, bp, stage)
				checks := 0
				bootstraps := 0
				api.execFn = func(ctx context.Context, id, command, name string) (client.ExecResult, error) {
					if command == tailscale.VerifyCommand {
						checks++
						if !healthy {
							return client.ExecResult{ExitCode: 1}, nil
						}
					}
					if command == "bootstrap-command" {
						bootstraps++
					}
					return client.ExecResult{}, nil
				}
				_, err := p.CreateInstance(context.Background(), bp)
				if (err == nil) != healthy || *mints != 0 || checks != 1 {
					t.Fatalf("unsafe network recovery: %v", err)
				}
				if healthy && bootstraps != 1 {
					t.Fatal("healthy network did not finish bootstrap")
				}
				if !healthy && (bootstraps != 0 || len(api.deletes) != 1) {
					t.Fatal("failed enrollment was not deleted before bootstrap")
				}
			})
		}
	}
}

func TestNetworkFailureAndLogoutErrorsStillDeleteVM(t *testing.T) {
	api := newFake()
	p, mints := networkProvider(t, api)
	logout := false
	bootstrapped := false
	api.execFn = func(ctx context.Context, id, command, name string) (client.ExecResult, error) {
		if command == tailscale.LogoutCommand {
			logout = true
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 5*time.Second {
				t.Error("logout is not bounded")
			}
			return client.ExecResult{}, errors.New("logout failed")
		}
		if strings.HasPrefix(name, "garm-network-") {
			return client.ExecResult{Stderr: "tskey-auth-worker-secret"}, errors.New("tskey-auth-worker-secret")
		}
		if command == "bootstrap-command" {
			bootstrapped = true
		}
		return client.ExecResult{}, nil
	}
	_, err := p.CreateInstance(context.Background(), testParams("failed-network"))
	if err == nil || strings.Contains(err.Error(), "tskey-auth-worker-secret") {
		t.Fatal("unsafe network error")
	}
	if *mints != 1 || !logout || bootstrapped || len(api.deletes) != 1 {
		t.Fatal("network failure did not safely delete VM")
	}
}

func TestNetworkConfigurationCannotChangeDuringRecovery(t *testing.T) {
	api := newFake()
	p, mints := networkProvider(t, api)
	bp := testParams("configuration")
	seedNetworkStage(t, p, api, bp, "networking")
	p.cfg.Tailscale.PrivateAliases = []string{"changed.example"}
	if _, err := p.CreateInstance(context.Background(), bp); err == nil {
		t.Fatal("network policy changed during recovery")
	}
	if *mints != 0 || api.creates != 1 {
		t.Fatal("configuration change caused new allocation")
	}
}
