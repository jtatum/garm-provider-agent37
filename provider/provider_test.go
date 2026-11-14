// Copyright 2026 The garm-provider-agent37 contributors
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	p.command = func(params.BootstrapInstance) (string, error) { return "bootstrap-command", nil }
	return p
}
func testParams(name string) params.BootstrapInstance {
	return params.BootstrapInstance{Name: name, PoolID: "pool-a", OSType: params.Linux, OSArch: params.Amd64, Image: "garm-runner@2", Flavor: "default"}
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
	other.command = p.command
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

func TestBootstrapFailureDeletesOwnedVMAndRedactsOutput(t *testing.T) {
	api := newFake()
	api.execFn = func(_ context.Context, _, cmd, _ string) (client.ExecResult, error) {
		if cmd == "bootstrap-command" {
			return client.ExecResult{ExitCode: 7, Stdout: "SECRET", Stderr: "SECRET"}, nil
		}
		return client.ExecResult{}, nil
	}
	p := testProvider(t, api)
	_, err := p.CreateInstance(context.Background(), testParams("runner-a"))
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("bad bootstrap error: %v", err)
	}
	if len(api.instances) != 0 || len(api.deletes) != 1 {
		t.Fatal("failed VM leaked")
	}
}

func TestCanceledReadinessDeletesOwnedVM(t *testing.T) {
	api := newFake()
	entered := make(chan struct{})
	api.execFn = func(ctx context.Context, _, _, _ string) (client.ExecResult, error) {
		close(entered)
		<-ctx.Done()
		return client.ExecResult{}, ctx.Err()
	}
	p := testProvider(t, api)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := p.CreateInstance(ctx, testParams("runner-a")); done <- err }()
	<-entered
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if len(api.instances) != 0 || len(api.deletes) != 1 {
		t.Fatal("canceled VM leaked")
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

func TestReadinessRetriesTransientFailure(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	attempt := 0
	api.execFn = func(_ context.Context, _, command, _ string) (client.ExecResult, error) {
		if command == bootstrap.ReadyCommand {
			attempt++
			if attempt == 1 {
				return client.ExecResult{}, &client.APIError{HTTPStatus: 409, Code: "try_again"}
			}
		}
		return client.ExecResult{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := p.CreateInstance(ctx, testParams("runner-a")); err != nil {
		t.Fatal(err)
	}
	if attempt != 2 {
		t.Fatalf("readiness attempts=%d", attempt)
	}
}

func TestRetryWithRefreshedTokenAdoptsLiveSupervisor(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	p.command = func(bp params.BootstrapInstance) (string, error) { return "bootstrap:" + bp.InstanceToken, nil }
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
	boots := 0
	resumes := 0
	for _, command := range api.execs {
		if strings.HasPrefix(command, "bootstrap:") {
			boots++
			if command != "bootstrap:first-token" {
				t.Fatal("bootstrap replayed refreshed token")
			}
		}
		if command == bootstrap.ResumeCommand {
			resumes++
		}
	}
	if boots != 1 || resumes != 1 {
		t.Fatalf("bootstrap=%d resume=%d", boots, resumes)
	}
}

func TestConcurrentDuplicateCreateOnlyBootstrapsOnce(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	other := newProvider(p.cfg, p.controllerID, p.poolID, api)
	other.command = p.command
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	api.execFn = func(ctx context.Context, _, command, _ string) (client.ExecResult, error) {
		if command == "bootstrap-command" {
			once.Do(func() { close(entered) })
			select {
			case <-release:
			case <-ctx.Done():
				return client.ExecResult{}, ctx.Err()
			}
		}
		return client.ExecResult{}, nil
	}
	done := make(chan error, 2)
	go func() { _, err := p.CreateInstance(context.Background(), testParams("runner-a")); done <- err }()
	<-entered
	called := make(chan struct{})
	go func() {
		close(called)
		_, err := other.CreateInstance(context.Background(), testParams("runner-a"))
		done <- err
	}()
	<-called
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	boots := 0
	for _, cmd := range api.execs {
		if cmd == "bootstrap-command" {
			boots++
		}
	}
	if api.creates != 1 || boots != 1 || len(api.deletes) != 0 {
		t.Fatalf("creates=%d bootstraps=%d deletes=%v", api.creates, boots, api.deletes)
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
