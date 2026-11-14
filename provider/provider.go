// Copyright 2026 The garm-provider-agent37 contributors
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	providerErrors "github.com/cloudbase/garm-provider-common/errors"
	"github.com/cloudbase/garm-provider-common/execution/common"
	executionv011 "github.com/cloudbase/garm-provider-common/execution/v0.1.1"
	"github.com/cloudbase/garm-provider-common/params"
	"github.com/jtatum/garm-provider-agent37/bootstrap"
	"github.com/jtatum/garm-provider-agent37/client"
	"github.com/jtatum/garm-provider-agent37/config"
	"github.com/jtatum/garm-provider-agent37/tailscale"
)

const (
	controllerKey = "garm-controller-id"
	providerKey   = "garm-provider-id"
	poolKey       = "garm-pool-id"
	nameKey       = "garm-runner-name"
	networkKey    = "garm-network"
)

var Version = "v0.1.0-dev"
var _ executionv011.ExternalProvider = (*Provider)(nil)
var identifierPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

type cloudAPI interface {
	Create(context.Context, client.CreateRequest) (client.Instance, error)
	List(context.Context) ([]client.Instance, error)
	Get(context.Context, string) (client.Instance, error)
	Delete(context.Context, string) error
	Exec(context.Context, string, string, string) (client.ExecResult, error)
}

type Provider struct {
	cfg            config.Config
	controllerID   string
	poolID         string
	statePath      string
	api            cloudAPI
	command        func(params.BootstrapInstance) (string, error)
	mintNetworkKey func(context.Context) (string, error)
	// beforeDeleteJournal is a test seam between DeleteInstance's cloud calls and its journal update.
	beforeDeleteJournal func()
}

func New(configPath, controllerID, poolID string) (*Provider, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("New: %w", err)
	}
	if !identifierPattern.MatchString(controllerID) || (poolID != "" && !identifierPattern.MatchString(poolID)) {
		return nil, fmt.Errorf("New: invalid controller or pool ID")
	}
	key, err := cfg.ReadKey()
	if err != nil {
		return nil, fmt.Errorf("New: %w", err)
	}
	api, err := client.New(key, "", "", nil)
	if err != nil {
		return nil, fmt.Errorf("New: %w", err)
	}
	return newProvider(cfg, controllerID, poolID, api), nil
}

func newProvider(cfg config.Config, controllerID, poolID string, api cloudAPI) *Provider {
	hash := sha256.Sum256([]byte(controllerID + "\x00" + cfg.ProviderID))
	return &Provider{cfg: cfg, controllerID: controllerID, poolID: poolID, api: api,
		statePath: filepath.Join(cfg.StateDir, hex.EncodeToString(hash[:16])), command: bootstrap.Command, mintNetworkKey: tailscale.New(cfg.Tailscale).Mint}
}

func (p *Provider) owns(instance client.Instance) bool {
	return instance.Metadata[controllerKey] == p.controllerID && instance.Metadata[providerKey] == p.cfg.ProviderID &&
		instance.Metadata[poolKey] != "" && instance.Metadata[nameKey] != ""
}

func (p *Provider) inPool(instance client.Instance) bool {
	return p.owns(instance) && (p.poolID == "" || instance.Metadata[poolKey] == p.poolID)
}

func (p *Provider) request(bp params.BootstrapInstance) (client.CreateRequest, error) {
	if bp.OSType != params.Linux || bp.OSArch != params.Amd64 {
		return client.CreateRequest{}, providerErrors.NewBadRequestError("request: only Linux amd64 VM workers are supported")
	}
	if !identifierPattern.MatchString(bp.Name) || !identifierPattern.MatchString(bp.PoolID) || (p.poolID != "" && bp.PoolID != p.poolID) {
		return client.CreateRequest{}, providerErrors.NewBadRequestError("request: invalid runner name or pool ID")
	}
	if len(bp.SSHKeys) != 0 || bp.JitConfigEnabled || bp.GitHubRunnerGroup != "" || bp.UserDataOptions.EnableBootDebug || len(bp.UserDataOptions.ExtraPackages) != 0 {
		return client.CreateRequest{}, providerErrors.NewBadRequestError("request: SSH injection, JIT, GitHub groups, boot debug and boot package installation are unsupported")
	}
	resources, err := p.cfg.Resources(bp.Image, bp.Flavor, string(bp.ExtraSpecs))
	if err != nil {
		return client.CreateRequest{}, providerErrors.NewBadRequestError("request: %s", err)
	}
	hash := sha256.Sum256([]byte(p.controllerID + "\x00" + p.cfg.ProviderID + "\x00" + bp.Name))
	metadata := map[string]string{controllerKey: p.controllerID, providerKey: p.cfg.ProviderID, poolKey: bp.PoolID, nameKey: bp.Name}
	if p.cfg.Tailscale.Enabled {
		network, _ := json.Marshal(p.cfg.Tailscale)
		metadata[networkKey] = fmt.Sprintf("tailscale-%x", sha256.Sum256(network))
	}
	return client.CreateRequest{Name: "garm-" + hex.EncodeToString(hash[:12]), Template: bp.Image, Type: "vm",
		Region: p.cfg.Region, Resources: resources, AutoSleep: false, MountPath: "/root",
		Metadata: metadata}, nil
}

func (p *Provider) reserve(ctx context.Context, bp params.BootstrapInstance, req client.CreateRequest) (client.Instance, bool, error) {
	unlock, err := p.lock(ctx)
	if err != nil {
		return client.Instance{}, false, fmt.Errorf("reserve: %w", err)
	}
	defer unlock()
	instances, err := p.api.List(ctx)
	if err != nil {
		return client.Instance{}, false, fmt.Errorf("reserve: list capacity: %w", err)
	}
	records, err := p.loadState()
	if err != nil {
		return client.Instance{}, false, fmt.Errorf("reserve: %w", err)
	}
	data, _ := json.Marshal(req)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(data))
	liveNames := map[string]bool{}
	var matches []client.Instance
	count := 0
	for _, instance := range instances {
		if !p.owns(instance) {
			continue
		}
		count++
		liveNames[instance.Metadata[nameKey]] = true
		if instance.Metadata[nameKey] == bp.Name {
			matches = append(matches, instance)
		}
	}
	if len(matches) > 1 {
		return client.Instance{}, false, fmt.Errorf("reserve: multiple owned VMs match this runner; refusing another create")
	}
	old, exists := records[bp.Name]
	if exists && (old.Fingerprint != fingerprint || old.PoolID != bp.PoolID) {
		return client.Instance{}, false, fmt.Errorf("reserve: runner name already belongs to a different creation request")
	}
	if len(matches) == 1 {
		instance := matches[0]
		if instance.Metadata[networkKey] != req.Metadata[networkKey] || instance.Metadata[poolKey] != bp.PoolID || instance.Template != req.Template || instance.Resources != req.Resources || instance.Type != "vm" || instance.AutoSleep || instance.MountPath != "/root" {
			return client.Instance{}, false, fmt.Errorf("reserve: existing VM does not match the requested worker")
		}
		if exists && (old.Stage == "deleted" || old.Stage == "failed") {
			return client.Instance{}, false, fmt.Errorf("reserve: retired runner cannot be reused")
		}
		stage := "recovered"
		if exists {
			stage = old.Stage
			if stage == "pending" || stage == "unknown" {
				stage = "allocated"
			}
		}
		records[bp.Name] = creation{Name: bp.Name, PoolID: bp.PoolID, Fingerprint: fingerprint, InstanceID: instance.ID, Stage: stage, UpdatedAt: time.Now().UTC()}
		if err := p.saveState(records); err != nil {
			return client.Instance{}, false, fmt.Errorf("reserve: %w", err)
		}
		return instance, false, nil
	}
	if exists && (old.Stage == "deleted" || old.Stage == "failed") {
		return client.Instance{}, false, fmt.Errorf("reserve: runner name was retired by an earlier create attempt and cannot be reused")
	}
	if exists {
		return client.Instance{}, false, fmt.Errorf("reserve: a creation intent already exists; reconcile or delete it before allocating another worker")
	}
	for name, record := range records {
		if liveNames[name] || record.Stage == "deleted" || record.Stage == "failed" {
			continue
		}
		if record.InstanceID == "" {
			count++
			continue
		}
		instance, err := p.api.Get(ctx, record.InstanceID)
		if client.IsNotFound(err) {
			continue
		}
		if err != nil {
			return client.Instance{}, false, fmt.Errorf("reserve: cannot verify allocated capacity: %w", err)
		}
		if !p.owns(instance) || instance.Metadata[nameKey] != name {
			return client.Instance{}, false, fmt.Errorf("reserve: allocated capacity ownership mismatch")
		}
		count++
	}
	if count >= p.cfg.MaxInstances {
		return client.Instance{}, false, providerErrors.NewNoCapacityError("reserve: provider capacity is reserved or in use")
	}
	records[bp.Name] = creation{Name: bp.Name, PoolID: bp.PoolID, Fingerprint: fingerprint, Stage: "pending", UpdatedAt: time.Now().UTC()}
	if err := p.saveState(records); err != nil {
		return client.Instance{}, false, fmt.Errorf("reserve: %w", err)
	}
	return client.Instance{}, true, nil
}

func (p *Provider) CreateInstance(ctx context.Context, bp params.BootstrapInstance) (params.ProviderInstance, error) {
	deadline, _ := time.ParseDuration(p.cfg.CreateTimeout)
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	req, err := p.request(bp)
	if err != nil {
		return params.ProviderInstance{}, fmt.Errorf("CreateInstance: %w", err)
	}
	command, err := p.command(bp)
	if err != nil {
		return params.ProviderInstance{}, fmt.Errorf("CreateInstance: bootstrap validation failed: %w", err)
	}
	runnerHash := sha256.Sum256([]byte(bp.Name))
	unlock, err := p.lockFile(ctx, runnerLockPrefix+hex.EncodeToString(runnerHash[:16])+".lock")
	if err != nil {
		return params.ProviderInstance{}, fmt.Errorf("CreateInstance: %w", err)
	}
	defer unlock()
	instance, create, err := p.reserve(ctx, bp, req)
	if err != nil {
		return params.ProviderInstance{}, fmt.Errorf("CreateInstance: %w", err)
	}
	if create {
		instance, err = p.api.Create(ctx, req)
		if err != nil {
			instance, err = p.reconcileCreate(bp, err)
			if err != nil {
				return params.ProviderInstance{}, fmt.Errorf("CreateInstance: %w", err)
			}
		}
		if !p.owns(instance) || instance.Metadata[nameKey] != bp.Name || instance.Metadata[networkKey] != req.Metadata[networkKey] || instance.Metadata[poolKey] != bp.PoolID || instance.Template != req.Template || instance.Resources != req.Resources || instance.Type != "vm" || instance.AutoSleep || instance.MountPath != "/root" {
			return params.ProviderInstance{}, fmt.Errorf("CreateInstance: API returned an unexpected VM; creation remains quarantined")
		}
		if err := p.record(ctx, bp.Name, instance.ID, "allocated"); err != nil {
			return params.ProviderInstance{}, p.failCreate(instance, err)
		}
	}
	if !create {
		stage, err := p.creationStage(ctx, bp.Name)
		if err != nil {
			return params.ProviderInstance{}, fmt.Errorf("CreateInstance: %w", err)
		}
		if stage == "launched" || stage == "launching" || stage == "recovered" {
			if p.cfg.Tailscale.Enabled {
				if err := p.verifyNetwork(ctx, instance.ID); err != nil {
					return params.ProviderInstance{}, fmt.Errorf("CreateInstance: network cannot be resumed: %w", err)
				}
			}
			if err := p.verifySupervisor(ctx, instance.ID); err != nil {
				return params.ProviderInstance{}, fmt.Errorf("CreateInstance: worker bootstrap was already attempted; refusing replay: %w", err)
			}
			if err := p.record(ctx, bp.Name, instance.ID, "launched"); err != nil {
				return params.ProviderInstance{}, fmt.Errorf("CreateInstance: %w", err)
			}
			return convert(instance), nil
		}
	}
	if err := p.ready(ctx, instance.ID); err != nil {
		return params.ProviderInstance{}, p.failCreate(instance, err)
	}
	if p.cfg.Tailscale.Enabled {
		if err := p.setupNetwork(ctx, bp.Name, instance); err != nil {
			return params.ProviderInstance{}, p.failCreate(instance, err)
		}
	}
	if err := p.record(ctx, bp.Name, instance.ID, "launching"); err != nil {
		return params.ProviderInstance{}, p.failCreate(instance, err)
	}
	r, err := p.api.Exec(ctx, instance.ID, command, "garm-bootstrap-"+instance.ID)
	if err != nil || r.ExitCode != 0 {
		if err == nil {
			err = fmt.Errorf("runner bootstrap command failed (exit %d)", r.ExitCode)
		}
		return params.ProviderInstance{}, p.failCreate(instance, err)
	}
	if err := p.record(ctx, bp.Name, instance.ID, "launched"); err != nil {
		return params.ProviderInstance{}, p.failCreate(instance, err)
	}
	instance.Status = "running"
	return convert(instance), nil
}

func (p *Provider) reconcileCreate(bp params.BootstrapInstance, original error) (client.Instance, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	instances, listErr := p.api.List(ctx)
	var matches []client.Instance
	for _, instance := range instances {
		if p.owns(instance) && instance.Metadata[nameKey] == bp.Name && instance.Metadata[poolKey] == bp.PoolID {
			matches = append(matches, instance)
		}
	}
	if listErr == nil && len(matches) == 1 {
		return matches[0], nil
	}
	stage := "unknown"
	var apiErr *client.APIError
	if errors.As(original, &apiErr) && ((apiErr.HTTPStatus >= 400 && apiErr.HTTPStatus < 500 && apiErr.HTTPStatus != 408) || (apiErr.HTTPStatus == 503 && apiErr.Code == "no_capacity")) {
		stage = "failed"
	}
	if err := p.record(ctx, bp.Name, "", stage); err != nil {
		return client.Instance{}, fmt.Errorf("reconcileCreate: creation unresolved and journal update failed: %w", err)
	}
	if apiErr != nil && (apiErr.HTTPStatus == 402 || apiErr.HTTPStatus == 429 || (apiErr.HTTPStatus == 503 && apiErr.Code == "no_capacity")) {
		return client.Instance{}, providerErrors.NewNoCapacityError("reconcileCreate: Agent37 declined capacity or funding")
	}
	return client.Instance{}, fmt.Errorf("reconcileCreate: creation was not retried; inspect owned inventory before clearing its intent: %w", original)
}

func (p *Provider) failCreate(instance client.Instance, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.DeleteInstance(ctx, instance.ID); err != nil {
		return fmt.Errorf("failCreate: bootstrap failed and owned VM cleanup failed: %w", errors.Join(cause, err))
	}
	return fmt.Errorf("failCreate: owned VM deleted after bootstrap failure: %w", cause)
}

func (p *Provider) ready(ctx context.Context, id string) error {
	poll, _ := time.ParseDuration(p.cfg.PollInterval)
	for attempt := 0; ; attempt++ {
		r, err := p.api.Exec(ctx, id, bootstrap.ReadyCommand, fmt.Sprintf("garm-ready-%d-%d", time.Now().UnixNano(), attempt))
		if err == nil && r.ExitCode == 0 {
			return nil
		}
		if err != nil && !client.IsRetryable(err) {
			var apiErr *client.APIError
			if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 409 {
				return fmt.Errorf("ready: %w", err)
			}
		}
		if err := wait(ctx, poll); err != nil {
			return fmt.Errorf("ready: Docker and runner readiness deadline: %w", err)
		}
	}
}

func (p *Provider) matches(ctx context.Context, identifier string) ([]client.Instance, error) {
	if !identifierPattern.MatchString(identifier) {
		return nil, providerErrors.NewBadRequestError("matches: invalid instance identifier")
	}
	instances, err := p.api.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("matches: %w", err)
	}
	var matches []client.Instance
	for _, instance := range instances {
		if instance.ID != identifier && instance.Metadata[nameKey] != identifier && instance.Name != identifier {
			continue
		}
		if !p.inPool(instance) {
			return nil, providerErrors.NewForbiddenError("matches: refusing access to an instance outside this controller/provider/pool")
		}
		matches = append(matches, instance)
	}
	if len(matches) == 0 {
		records, err := p.loadState()
		if err != nil {
			return nil, fmt.Errorf("matches: %w", err)
		}
		knownID := ""
		for name, record := range records {
			if name == identifier || record.InstanceID == identifier {
				knownID = record.InstanceID
				break
			}
		}
		if knownID != "" {
			instance, err := p.api.Get(ctx, knownID)
			if client.IsNotFound(err) {
				return matches, nil
			}
			if err != nil {
				return nil, fmt.Errorf("matches: verify known instance: %w", err)
			}
			if !p.inPool(instance) {
				return nil, providerErrors.NewForbiddenError("matches: known VM ownership changed")
			}
			matches = append(matches, instance)
		}
	}
	return matches, nil
}

func (p *Provider) DeleteInstance(ctx context.Context, identifier string) error {
	err := p.deleteInstance(ctx, identifier)
	p.sweepRunnerLocks()
	return err
}

// deleteInstance performs cloud calls without the journal lock, which every concurrent create
// and delete shares; holding it across logout and DELETE serialized bursts behind slow API
// calls. Decisions still use a consistent view: the journal is snapshotted before inventory is
// read, and under the lock any relevant record that changed since then, other than one whose
// VM this call deleted, aborts the update so GARM retries against fresh inventory.
func (p *Provider) deleteInstance(ctx context.Context, identifier string) error {
	snapshot, err := p.loadState()
	if err != nil {
		return fmt.Errorf("DeleteInstance: %w", err)
	}
	instances, err := p.matches(ctx, identifier)
	if err != nil {
		return fmt.Errorf("DeleteInstance: %w", err)
	}
	removedIDs, removedNames := map[string]bool{}, map[string]bool{}
	for _, instance := range instances {
		if instance.Metadata[networkKey] != "" {
			logoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, _ = p.api.Exec(logoutCtx, instance.ID, tailscale.LogoutCommand, fmt.Sprintf("garm-logout-%d", time.Now().UnixNano()))
			cancel()
		}
		if err := p.api.Delete(ctx, instance.ID); err != nil && !client.IsNotFound(err) {
			return fmt.Errorf("DeleteInstance: delete owned VM: %w", err)
		}
		removedIDs[instance.ID], removedNames[instance.Metadata[nameKey]] = true, true
	}
	if p.beforeDeleteJournal != nil {
		p.beforeDeleteJournal()
	}
	ctx, cancel := journalContext(ctx)
	defer cancel()
	unlock, err := p.lock(ctx)
	if err != nil {
		return fmt.Errorf("DeleteInstance: %w", err)
	}
	defer unlock()
	records, err := p.loadState()
	if err != nil {
		return fmt.Errorf("DeleteInstance: %w", err)
	}
	for name, record := range records {
		if name != identifier && record.InstanceID != identifier && !removedNames[name] {
			continue
		}
		if record.InstanceID != "" && removedIDs[record.InstanceID] {
			continue
		}
		if before, ok := snapshot[name]; !ok || before.Stage != record.Stage || before.InstanceID != record.InstanceID {
			return fmt.Errorf("DeleteInstance: creation advanced during deletion; retry against current inventory")
		}
	}
	for name, record := range records {
		if removedIDs[record.InstanceID] || removedNames[name] {
			record.Stage, record.UpdatedAt = "deleted", time.Now().UTC()
			records[name] = record
		}
	}
	for name, record := range records {
		if name != identifier && record.InstanceID != identifier {
			continue
		}
		if p.poolID != "" && record.PoolID != p.poolID {
			return providerErrors.NewForbiddenError("DeleteInstance: creation intent belongs to another pool")
		}
		if len(instances) == 0 && (record.Stage == "pending" || record.Stage == "unknown") {
			return fmt.Errorf("DeleteInstance: creation outcome is still ambiguous; intent remains quarantined")
		}
		record.Stage, record.UpdatedAt = "deleted", time.Now().UTC()
		records[name] = record
	}
	if err := p.saveState(records); err != nil {
		return fmt.Errorf("DeleteInstance: %w", err)
	}
	return nil
}

func (p *Provider) GetInstance(ctx context.Context, identifier string) (params.ProviderInstance, error) {
	instances, err := p.matches(ctx, identifier)
	if err != nil {
		return params.ProviderInstance{}, fmt.Errorf("GetInstance: %w", err)
	}
	if len(instances) == 0 {
		return params.ProviderInstance{}, providerErrors.NewNotFoundError("GetInstance: owned VM not found")
	}
	if len(instances) > 1 {
		return params.ProviderInstance{}, fmt.Errorf("GetInstance: multiple owned VMs match the runner name")
	}
	return convert(instances[0]), nil
}

func (p *Provider) ListInstances(ctx context.Context, poolID string) ([]params.ProviderInstance, error) {
	if !identifierPattern.MatchString(poolID) || (p.poolID != "" && poolID != p.poolID) {
		return nil, providerErrors.NewBadRequestError("ListInstances: invalid pool ID")
	}
	instances, err := p.api.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListInstances: %w", err)
	}
	result := []params.ProviderInstance{}
	for _, instance := range instances {
		if p.owns(instance) && instance.Metadata[poolKey] == poolID {
			result = append(result, convert(instance))
		}
	}
	return result, nil
}

func (p *Provider) RemoveAllInstances(ctx context.Context) error {
	instances, err := p.api.List(ctx)
	if err != nil {
		return fmt.Errorf("RemoveAllInstances: %w", err)
	}
	var failures []error
	allPools := *p
	allPools.poolID = ""
	for _, instance := range instances {
		if p.owns(instance) {
			if err := allPools.DeleteInstance(ctx, instance.ID); err != nil {
				failures = append(failures, err)
			}
		}
	}
	if err := errors.Join(failures...); err != nil {
		return fmt.Errorf("RemoveAllInstances: %w", err)
	}
	return nil
}

func (p *Provider) verifySupervisor(ctx context.Context, id string) error {
	poll, _ := time.ParseDuration(p.cfg.PollInterval)
	for {
		result, err := p.api.Exec(ctx, id, bootstrap.ResumeCommand, fmt.Sprintf("garm-verify-%d", time.Now().UnixNano()))
		if err == nil {
			if result.ExitCode != 0 {
				return providerErrors.NewBadRequestError("verifySupervisor: worker finished or was interrupted; replace it")
			}
			return nil
		}
		var apiErr *client.APIError
		if !client.IsRetryable(err) && !(errors.As(err, &apiErr) && apiErr.HTTPStatus == 409) {
			return fmt.Errorf("verifySupervisor: %w", err)
		}
		if err := wait(ctx, poll); err != nil {
			return fmt.Errorf("verifySupervisor: %w", err)
		}
	}
}

func (p *Provider) Start(ctx context.Context, identifier string) error {
	instances, err := p.matches(ctx, identifier)
	if err != nil {
		return fmt.Errorf("Start: %w", err)
	}
	if len(instances) == 0 {
		return providerErrors.NewNotFoundError("Start: owned VM not found")
	}
	if len(instances) != 1 || instances[0].Status != "running" {
		return providerErrors.NewBadRequestError("Start: stopped or interrupted workers must be deleted and replaced")
	}
	r, err := p.api.Exec(ctx, instances[0].ID, bootstrap.ResumeCommand, fmt.Sprintf("garm-verify-%d", time.Now().UnixNano()))
	if err != nil {
		return fmt.Errorf("Start: %w", err)
	}
	if r.ExitCode != 0 {
		return providerErrors.NewBadRequestError("Start: completed or interrupted workers must be deleted and replaced")
	}
	return nil
}

func (p *Provider) Stop(ctx context.Context, identifier string, force bool) error {
	if _, err := p.matches(ctx, identifier); err != nil {
		return fmt.Errorf("Stop: %w", err)
	}
	return providerErrors.NewBadRequestError("Stop: suspending one-job workers is unsupported; delete the worker instead")
}

func (p *Provider) GetVersion(context.Context) string { return Version }

func (p *Provider) GetSupportedInterfaceVersions(context.Context) []string {
	return []string{common.Version010, common.Version011}
}

func (p *Provider) ValidatePoolInfo(ctx context.Context, image, flavor, providerConfig, extraSpecs string) error {
	cfg := p.cfg
	if providerConfig != "" {
		var err error
		cfg, err = config.Load(providerConfig)
		if err != nil {
			return fmt.Errorf("ValidatePoolInfo: %w", err)
		}
	}
	if _, err := cfg.Resources(image, flavor, extraSpecs); err != nil {
		return providerErrors.NewBadRequestError("ValidatePoolInfo: %s", err)
	}
	return nil
}

func (p *Provider) GetConfigJSONSchema(context.Context) (string, error) {
	return config.JSONSchema, nil
}

func (p *Provider) GetExtraSpecsJSONSchema(context.Context) (string, error) {
	return config.ExtraSpecsJSONSchema, nil
}

func convert(instance client.Instance) params.ProviderInstance {
	status := params.InstanceStatusUnknown
	switch instance.Status {
	case "running":
		status = params.InstanceRunning
	case "stopped", "sleeping":
		status = params.InstanceStopped
	case "failed", "error":
		status = params.InstanceError
	}
	return params.ProviderInstance{ProviderID: instance.ID, Name: instance.Metadata[nameKey], OSType: params.Linux,
		OSArch: params.Amd64, Status: status}
}
