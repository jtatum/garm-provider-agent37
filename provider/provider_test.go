// Copyright 2026 The garm-provider-agent37 contributors
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	providerErrors "github.com/cloudbase/garm-provider-common/errors"
	"github.com/cloudbase/garm-provider-common/params"
	"github.com/jtatum/garm-provider-agent37/bootstrap"
	"github.com/jtatum/garm-provider-agent37/client"
	"github.com/jtatum/garm-provider-agent37/config"
)

type fakeAPI struct {
	mu             sync.Mutex
	instances      map[string]client.Instance
	creates        int
	deletes        []string
	execs          []string
	requests       []client.CreateRequest
	createError    error
	createPersists bool
	createStarted  chan struct{}
	createContinue chan struct{}
	deleteStarted  chan struct{}
	deleteContinue chan struct{}
	execFn         func(context.Context, string, string, string) (client.ExecResult, error)
}

func newFake() *fakeAPI {
	return &fakeAPI{instances: map[string]client.Instance{}, createPersists: true}
}
func (f *fakeAPI) List(ctx context.Context) ([]client.Instance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := []client.Instance{}
	for _, v := range f.instances {
		rows = append(rows, v)
	}
	return rows, nil
}
func (f *fakeAPI) Get(ctx context.Context, id string) (client.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.instances[id]
	if !ok {
		return client.Instance{}, &client.APIError{HTTPStatus: 404, Code: "not_found"}
	}
	return i, nil
}
func (f *fakeAPI) Create(ctx context.Context, r client.CreateRequest) (client.Instance, error) {
	f.mu.Lock()
	f.creates++
	f.requests = append(f.requests, r)
	id := fmt.Sprintf("test%06d", f.creates)
	obj := client.Instance{ID: id, Name: r.Name, Template: r.Template, Type: r.Type, Region: r.Region, MountPath: r.MountPath, Status: "running", Resources: r.Resources, Metadata: r.Metadata, AutoSleep: r.AutoSleep}
	if f.createPersists {
		f.instances[id] = obj
	}
	f.mu.Unlock()
	if f.createStarted != nil {
		close(f.createStarted)
	}
	if f.createContinue != nil {
		select {
		case <-f.createContinue:
		case <-ctx.Done():
			return client.Instance{}, ctx.Err()
		}
	}
	if f.createError != nil {
		return client.Instance{}, f.createError
	}
	return obj, nil
}
func (f *fakeAPI) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.deleteStarted != nil {
		close(f.deleteStarted)
		select {
		case <-f.deleteContinue:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, id)
	delete(f.instances, id)
	return nil
}
func (f *fakeAPI) Exec(ctx context.Context, id, command, name string) (client.ExecResult, error) {
	f.mu.Lock()
	f.execs = append(f.execs, command)
	f.mu.Unlock()
	if f.execFn != nil {
		return f.execFn(ctx, id, command, name)
	}
	return client.ExecResult{}, nil
}

func testProvider(t *testing.T, api *fakeAPI) *Provider {
	t.Helper()
	cfg := config.Defaults()
	cfg.ProviderID = "lab"
	cfg.StateDir = t.TempDir()
	cfg.APIKeyFile = "/unused"
	cfg.PollInterval = "100ms"
	if err := os.Chmod(cfg.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := newProvider(cfg, "controller-a", "pool-a", api)
	return p
}
func testParams(name string) params.BootstrapInstance {
	return params.BootstrapInstance{Name: name, PoolID: "pool-a", InstanceToken: "private-token", MetadataURL: "http://metadata.invalid", CallbackURL: "http://callback.invalid", OSType: params.Linux, OSArch: params.Amd64, Image: "garm-runner@2", Flavor: "default"}
}
func owned(id, name, pool, controller, provider string) client.Instance {
	return client.Instance{ID: id, Name: "cloud-" + id, Status: "running", Type: "vm", Template: "garm-runner@2", MountPath: "/root", Resources: client.Resources{CPU: 4, Memory: 8, Disk: 20}, Metadata: map[string]string{controllerKey: controller, providerKey: provider, poolKey: pool, nameKey: name}}
}

func TestCreateRetryAndScope(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	ctx := context.Background()
	bp := testParams("runner-a")
	first, err := p.CreateInstance(ctx, bp)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.CreateInstance(ctx, bp)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProviderID != second.ProviderID || api.creates != 1 {
		t.Fatalf("duplicate create: %+v %+v", first, second)
	}
	if first.Name != bp.Name || first.Status != params.InstanceRunning {
		t.Fatalf("bad protocol result: %+v", first)
	}
	if strings.Contains(first.Name, "cloud-") {
		t.Fatal("cloud name leaked instead of GARM name")
	}
	if err := p.DeleteInstance(ctx, bp.Name); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteInstance(ctx, bp.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CreateInstance(ctx, bp); err == nil {
		t.Fatal("retired name reused")
	}
	if api.creates != 1 {
		t.Fatal("retired name allocated another VM")
	}
}

func TestOwnershipAndPoolFilter(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	ctx := context.Background()
	api.instances["own0000001"] = owned("own0000001", "runner-a", "pool-a", "controller-a", "lab")
	api.instances["own0000002"] = owned("own0000002", "runner-b", "pool-b", "controller-a", "lab")
	api.instances["other00001"] = owned("other00001", "runner-c", "pool-a", "other-controller", "lab")
	api.instances["other00002"] = owned("other00002", "runner-d", "pool-a", "controller-a", "other-provider")
	rows, err := p.ListInstances(ctx, "pool-a")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	for _, id := range []string{"own0000002", "other00001", "other00002"} {
		if err := p.DeleteInstance(ctx, id); !errors.Is(err, providerErrors.ErrForbidden) {
			t.Fatalf("foreign deletion not rejected: %v", err)
		}
	}
	if err := p.RemoveAllInstances(ctx); err != nil {
		t.Fatal(err)
	}
	if len(api.instances) != 2 || len(api.deletes) != 2 {
		t.Fatalf("foreign resources changed: %+v", api.instances)
	}
	if _, err := p.GetInstance(ctx, "missing"); !errors.Is(err, providerErrors.ErrNotFound) {
		t.Fatalf("missing Get: %v", err)
	}
	if err := p.DeleteInstance(ctx, "missing"); err != nil {
		t.Fatal(err)
	}
}

func TestAmbiguousCreateReconcilesWithoutSecondPOST(t *testing.T) {
	api := newFake()
	api.createError = errors.New("connection lost")
	p := testProvider(t, api)
	i, err := p.CreateInstance(context.Background(), testParams("runner-a"))
	if err != nil {
		t.Fatal(err)
	}
	if api.creates != 1 || i.ProviderID == "" {
		t.Fatalf("create not reconciled: %+v", i)
	}
}

func TestUnknownCreationIsQuarantinedAndCountsCapacity(t *testing.T) {
	api := newFake()
	api.createError = errors.New("connection lost")
	api.createPersists = false
	p := testProvider(t, api)
	p.cfg.MaxInstances = 1
	ctx := context.Background()
	bp := testParams("runner-a")
	if _, err := p.CreateInstance(ctx, bp); err == nil {
		t.Fatal("unknown creation accepted")
	}
	if _, err := p.CreateInstance(ctx, bp); err == nil {
		t.Fatal("unknown creation retried")
	}
	if _, err := p.CreateInstance(ctx, testParams("runner-b")); !errors.Is(err, providerErrors.ErrNoCapacity) {
		t.Fatalf("unknown reservation not counted: %v", err)
	}
	if err := p.DeleteInstance(ctx, bp.Name); err == nil {
		t.Fatal("ambiguous cleanup claimed success")
	}
	if api.creates != 1 {
		t.Fatalf("POST replayed %d times", api.creates)
	}
	api.instances["late000001"] = owned("late000001", bp.Name, bp.PoolID, p.controllerID, p.cfg.ProviderID)
	if err := p.DeleteInstance(ctx, bp.Name); err != nil {
		t.Fatal(err)
	}
	if len(api.instances) != 0 {
		t.Fatal("late VM leaked")
	}
}

func TestConcurrentReservationsEnforceCap(t *testing.T) {
	api := newFake()
	api.createStarted = make(chan struct{})
	api.createContinue = make(chan struct{})
	api.createPersists = false
	p := testProvider(t, api)
	p.cfg.MaxInstances = 1
	other := newProvider(p.cfg, p.controllerID, p.poolID, api)
	done := make(chan error, 1)
	go func() { _, err := p.CreateInstance(context.Background(), testParams("runner-a")); done <- err }()
	<-api.createStarted
	_, err := other.CreateInstance(context.Background(), testParams("runner-b"))
	if !errors.Is(err, providerErrors.ErrNoCapacity) {
		t.Fatalf("concurrent reservation exceeded capacity: %v", err)
	}
	close(api.createContinue)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if api.creates != 1 {
		t.Fatal("concurrent POST exceeded capacity")
	}
}

func TestCreateReturnsBeforeGuestIsReachable(t *testing.T) {
	api := newFake()
	api.execFn = func(context.Context, string, string, string) (client.ExecResult, error) {
		t.Fatal("creation must not wait for or execute guest bootstrap")
		return client.ExecResult{}, nil
	}
	p := testProvider(t, api)
	bp := testParams("runner-a")
	instance, err := p.CreateInstance(context.Background(), bp)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(api.requests[0].Env[bootstrap.EnvironmentKey]), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["token"] != bp.InstanceToken || cfg["name"] != bp.Name || cfg["metadata_url"] != bp.MetadataURL {
		t.Fatal("bootstrap configuration was not handed to the VM")
	}
	records, err := p.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if records[bp.Name].InstanceID != instance.ProviderID || records[bp.Name].Stage != "allocated" {
		t.Fatal("VM identity was not durably recorded before return")
	}
	data, _ := json.Marshal(records)
	if strings.Contains(string(data), bp.InstanceToken) {
		t.Fatal("bootstrap token entered journal")
	}
}

func TestDeleteNameRemovesOnlyOwnedDuplicates(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	api.instances["own0000001"] = owned("own0000001", "runner-a", "pool-a", "controller-a", "lab")
	api.instances["own0000002"] = owned("own0000002", "runner-a", "pool-a", "controller-a", "lab")
	if _, err := p.GetInstance(context.Background(), "runner-a"); err == nil {
		t.Fatal("ambiguous Get accepted")
	}
	if _, err := p.CreateInstance(context.Background(), testParams("runner-a")); err == nil {
		t.Fatal("ambiguous Create accepted")
	}
	if err := p.DeleteInstance(context.Background(), "runner-a"); err != nil {
		t.Fatal(err)
	}
	if len(api.instances) != 0 || len(api.deletes) != 2 {
		t.Fatal("owned duplicate cleanup incomplete")
	}
}

func TestStartStopFailClosed(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	ctx := context.Background()
	i := owned("own0000001", "runner-a", "pool-a", "controller-a", "lab")
	api.instances[i.ID] = i
	if err := p.Start(ctx, i.ID); err != nil {
		t.Fatal(err)
	}
	if len(api.execs) != 1 || api.execs[0] != bootstrap.ResumeCommand {
		t.Fatal("Start skipped live-supervisor verification")
	}
	i.Status = "stopped"
	api.instances[i.ID] = i
	if err := p.Start(ctx, i.ID); !errors.Is(err, providerErrors.ErrBadRequest) {
		t.Fatalf("stopped VM restart allowed: %v", err)
	}
	if err := p.Stop(ctx, i.ID, true); !errors.Is(err, providerErrors.ErrBadRequest) {
		t.Fatalf("Stop allowed: %v", err)
	}
	i.Status = "running"
	api.instances[i.ID] = i
	api.execFn = func(context.Context, string, string, string) (client.ExecResult, error) {
		return client.ExecResult{ExitCode: 1}, nil
	}
	if err := p.Start(ctx, i.ID); !errors.Is(err, providerErrors.ErrBadRequest) {
		t.Fatalf("completed supervisor restart allowed: %v", err)
	}
}

func TestInvalidParamsNeverAllocate(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	for _, modify := range []func(*params.BootstrapInstance){
		func(b *params.BootstrapInstance) { b.OSArch = params.Arm64 }, func(b *params.BootstrapInstance) { b.OSType = params.Windows },
		func(b *params.BootstrapInstance) { b.JitConfigEnabled = true }, func(b *params.BootstrapInstance) { b.SSHKeys = []string{"x"} },
		func(b *params.BootstrapInstance) { b.UserDataOptions.EnableBootDebug = true }, func(b *params.BootstrapInstance) { b.PoolID = "other" },
		func(b *params.BootstrapInstance) { b.Image = "garm-runner" },
	} {
		b := testParams("runner-a")
		modify(&b)
		if _, err := p.CreateInstance(context.Background(), b); err == nil {
			t.Fatalf("accepted invalid params: %+v", b)
		}
	}
	if api.creates != 0 {
		t.Fatal("invalid request allocated VM")
	}
}

func TestStateCorruptionAndLockCancellation(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	ctx := context.Background()
	unlock, err := p.lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.lock(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("lock ignored cancel: %v", err)
	}
	unlock()
	if err := os.WriteFile(filepath.Join(p.statePath, "creations.json"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CreateInstance(ctx, testParams("runner-a")); err == nil {
		t.Fatal("corrupt journal accepted")
	}
	if api.creates != 0 {
		t.Fatal("allocated with corrupt journal")
	}
}

func TestProviderStatusDoesNotUseGARMPendingStates(t *testing.T) {
	for _, state := range []string{"starting", "creating", "provisioning", "deleting"} {
		if got := convert(client.Instance{Status: state}).Status; got != params.InstanceStatusUnknown {
			t.Fatalf("%s -> %s", state, got)
		}
	}
}

func TestNoCapacityAPIErrorDoesNotReserveForever(t *testing.T) {
	api := newFake()
	api.createPersists = false
	api.createError = &client.APIError{HTTPStatus: 429, Code: "rate_limit_exceeded"}
	p := testProvider(t, api)
	p.cfg.MaxInstances = 1
	if _, err := p.CreateInstance(context.Background(), testParams("runner-a")); !errors.Is(err, providerErrors.ErrNoCapacity) {
		t.Fatalf("capacity error not mapped: %v", err)
	}
	if err := p.DeleteInstance(context.Background(), "runner-a"); err != nil {
		t.Fatal(err)
	}
	api.createError = nil
	api.createPersists = true
	if _, err := p.CreateInstance(context.Background(), testParams("runner-b")); err != nil {
		t.Fatal(err)
	}
}

func TestDeletedIntentCannotBeResurrected(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	ctx := context.Background()
	if _, err := p.CreateInstance(ctx, testParams("runner-a")); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteInstance(ctx, "runner-a"); err != nil {
		t.Fatal(err)
	}
	if err := p.record(ctx, "runner-a", "late000001", "allocated"); err == nil {
		t.Fatal("deleted journal record resurrected")
	}
}

func TestRetryWithRefreshedTokenAdoptsExistingVM(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	bp := testParams("runner-a")
	bp.InstanceToken = "first-token"
	first, err := p.CreateInstance(context.Background(), bp)
	if err != nil {
		t.Fatal(err)
	}
	bp.InstanceToken = "refreshed-token"
	second, err := p.CreateInstance(context.Background(), bp)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProviderID != second.ProviderID || api.creates != 1 || len(api.deletes) != 0 {
		t.Fatal("retry replaced healthy VM")
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(api.requests[0].Env[bootstrap.EnvironmentKey]), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["token"] != "first-token" || len(api.execs) != 0 {
		t.Fatal("retry replayed or changed guest bootstrap")
	}
}

func TestConcurrentDuplicateCreateOnlyAllocatesOnce(t *testing.T) {
	api := newFake()
	api.createStarted = make(chan struct{})
	api.createContinue = make(chan struct{})
	p := testProvider(t, api)
	other := newProvider(p.cfg, p.controllerID, p.poolID, api)
	done := make(chan error, 2)
	go func() { _, err := p.CreateInstance(context.Background(), testParams("runner-a")); done <- err }()
	<-api.createStarted
	go func() { _, err := other.CreateInstance(context.Background(), testParams("runner-a")); done <- err }()
	close(api.createContinue)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if api.creates != 1 || len(api.execs) != 0 || len(api.deletes) != 0 {
		t.Fatal("concurrent retry duplicated or replayed bootstrap")
	}
}

func TestRecoveryRequiresEntrypointContract(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	bp := testParams("runner-a")
	first, err := p.CreateInstance(context.Background(), bp)
	if err != nil {
		t.Fatal(err)
	}
	delete(api.instances[first.ProviderID].Metadata, bootstrapKey)
	if _, err := p.CreateInstance(context.Background(), bp); err == nil {
		t.Fatal("legacy VM adopted without entrypoint bootstrap")
	}
	if api.creates != 1 || len(api.execs) != 0 {
		t.Fatal("legacy recovery attempted bootstrap")
	}
}

func TestKnownNoCapacity503ReleasesReservation(t *testing.T) {
	api := newFake()
	api.createPersists = false
	api.createError = &client.APIError{HTTPStatus: 503, Code: "no_capacity"}
	p := testProvider(t, api)
	p.cfg.MaxInstances = 1
	if _, err := p.CreateInstance(context.Background(), testParams("runner-a")); !errors.Is(err, providerErrors.ErrNoCapacity) {
		t.Fatalf("no_capacity not mapped: %v", err)
	}
	api.createError = nil
	api.createPersists = true
	if _, err := p.CreateInstance(context.Background(), testParams("runner-b")); err != nil {
		t.Fatalf("definitive rejection retained capacity: %v", err)
	}
}

func TestUnknown503StillQuarantinesReservation(t *testing.T) {
	api := newFake()
	api.createPersists = false
	api.createError = &client.APIError{HTTPStatus: 503, Code: "service_unavailable"}
	p := testProvider(t, api)
	p.cfg.MaxInstances = 1
	if _, err := p.CreateInstance(context.Background(), testParams("runner-a")); err == nil {
		t.Fatal("unknown503 accepted")
	}
	if _, err := p.CreateInstance(context.Background(), testParams("runner-b")); !errors.Is(err, providerErrors.ErrNoCapacity) {
		t.Fatalf("unknown503 released capacity: %v", err)
	}
}

type laggedInventory struct {
	*fakeAPI
	hide bool
}

func (a *laggedInventory) List(ctx context.Context) ([]client.Instance, error) {
	if a.hide {
		return []client.Instance{}, nil
	}
	return a.fakeAPI.List(ctx)
}

func TestMissingListEntryRetainsKnownCapacityAndCanBeDeleted(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	p.cfg.MaxInstances = 1
	instance, err := p.CreateInstance(context.Background(), testParams("runner-a"))
	if err != nil {
		t.Fatal(err)
	}
	lagged := &laggedInventory{fakeAPI: api, hide: true}
	p.api = lagged
	if _, err := p.CreateInstance(context.Background(), testParams("runner-b")); !errors.Is(err, providerErrors.ErrNoCapacity) {
		t.Fatalf("hidden allocated VM did not reserve capacity: %v", err)
	}
	if err := p.DeleteInstance(context.Background(), "runner-a"); err != nil {
		t.Fatal(err)
	}
	if len(api.instances) != 0 || len(api.deletes) != 1 || api.deletes[0] != instance.ProviderID {
		t.Fatal("hidden known VM was not deleted")
	}
}
