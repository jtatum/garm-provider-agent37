// Copyright 2026 The garm-provider-agent37 contributors
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/jtatum/garm-provider-agent37/client"
	"github.com/jtatum/garm-provider-agent37/tailscale"
)

func (p *Provider) setupNetwork(ctx context.Context, name string, instance client.Instance) error {
	stage, err := p.creationStage(ctx, name)
	if err != nil {
		return fmt.Errorf("setupNetwork: %w", err)
	}
	if stage == "networking" || stage == "networked" {
		if err := p.verifyNetwork(ctx, instance.ID); err != nil {
			return fmt.Errorf("setupNetwork: enrollment was already attempted; refusing replay: %w", err)
		}
		return p.record(ctx, name, instance.ID, "networked")
	}
	if stage != "allocated" {
		return fmt.Errorf("setupNetwork: unexpected creation stage")
	}
	if err := p.record(ctx, name, instance.ID, "networking"); err != nil {
		return fmt.Errorf("setupNetwork: %w", err)
	}
	key, err := p.mintNetworkKey(ctx)
	if err != nil {
		return fmt.Errorf("setupNetwork: %w", err)
	}
	command, err := tailscale.Command(p.cfg.Tailscale, key, instance.Name)
	if err != nil {
		return fmt.Errorf("setupNetwork: %w", err)
	}
	result, err := p.api.Exec(ctx, instance.ID, command, "garm-network-"+instance.ID)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("setupNetwork: guest network enrollment failed")
	}
	if err := p.record(ctx, name, instance.ID, "networked"); err != nil {
		return fmt.Errorf("setupNetwork: %w", err)
	}
	return nil
}

func (p *Provider) verifyNetwork(ctx context.Context, id string) error {
	result, err := p.api.Exec(ctx, id, tailscale.VerifyCommand, fmt.Sprintf("garm-network-verify-%d", time.Now().UnixNano()))
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("verifyNetwork: guest network is unavailable")
	}
	return nil
}
