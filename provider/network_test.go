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

func TestNetworkConfigurationIsSentOnceAndNotJournaled(t *testing.T) {
	api := newFake()
	p, mints := networkProvider(t, api)
	bp := testParams("runner-network")
	for range 2 {
		if _, err := p.CreateInstance(context.Background(), bp); err != nil {
			t.Fatal(err)
		}
	}
	if *mints != 1 || api.creates != 1 || len(api.execs) != 0 {
		t.Fatal("network enrollment replayed")
	}
	payload := api.requests[0].Env[tailscale.EnvironmentKey]
	if !strings.Contains(payload, "tskey-auth-worker-secret") || strings.Contains(payload, "controller-secret") || strings.Contains(payload, "controller-client") {
		t.Fatal("incorrect guest credential scope")
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

func TestNetworkKeyFailureReleasesCapacityWithoutCreatingVM(t *testing.T) {
	api := newFake()
	p, _ := networkProvider(t, api)
	p.cfg.MaxInstances = 1
	p.mintNetworkKey = func(context.Context) (string, error) { return "", errors.New("mint failed") }
	if _, err := p.CreateInstance(context.Background(), testParams("failed-network")); err == nil {
		t.Fatal("key failure ignored")
	}
	if api.creates != 0 {
		t.Fatal("VM created without network key")
	}
	p.mintNetworkKey = func(context.Context) (string, error) { return "tskey-auth-worker-secret", nil }
	if _, err := p.CreateInstance(context.Background(), testParams("next-worker")); err != nil {
		t.Fatal(err)
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

func TestLogoutErrorsStillDeleteVM(t *testing.T) {
	api := newFake()
	p, _ := networkProvider(t, api)
	instance, err := p.CreateInstance(context.Background(), testParams("runner-network"))
	if err != nil {
		t.Fatal(err)
	}
	logout := false
	api.execFn = func(ctx context.Context, id, command, name string) (client.ExecResult, error) {
		if command == tailscale.LogoutCommand {
			logout = true
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 5*time.Second {
				t.Error("logout is not bounded")
			}
			return client.ExecResult{}, errors.New("logout failed")
		}
		return client.ExecResult{}, nil
	}
	if err := p.DeleteInstance(context.Background(), instance.ProviderID); err != nil {
		t.Fatal(err)
	}
	if !logout || len(api.deletes) != 1 {
		t.Fatal("logout failure prevented deletion")
	}
}

func TestNetworkConfigurationCannotChangeDuringRecovery(t *testing.T) {
	api := newFake()
	p, mints := networkProvider(t, api)
	bp := testParams("configuration")
	seedNetworkStage(t, p, api, bp, "allocated")
	p.cfg.Tailscale.PrivateAliases = []string{"changed.example"}
	if _, err := p.CreateInstance(context.Background(), bp); err == nil {
		t.Fatal("network policy changed during recovery")
	}
	if *mints != 0 || api.creates != 1 {
		t.Fatal("configuration change caused new allocation")
	}
}
