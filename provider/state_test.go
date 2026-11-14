// Copyright 2026 The garm-provider-agent37 contributors
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runnerLocks(t *testing.T, p *Provider) []string {
	t.Helper()
	entries, err := os.ReadDir(p.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), runnerLockPrefix) {
			names = append(names, entry.Name())
		}
	}
	return names
}

func TestCompactionDropsOnlyExpiredRetiredRecords(t *testing.T) {
	p := testProvider(t, newFake())
	old := time.Now().UTC().Add(-retiredRetention - time.Hour)
	recent := time.Now().UTC().Add(-time.Hour)
	records := map[string]creation{}
	for _, r := range []creation{
		{Name: "old-deleted", Stage: "deleted", UpdatedAt: old},
		{Name: "old-failed", Stage: "failed", UpdatedAt: old},
		{Name: "recent-deleted", Stage: "deleted", UpdatedAt: recent},
		{Name: "old-pending", Stage: "pending", UpdatedAt: old},
		{Name: "old-unknown", Stage: "unknown", UpdatedAt: old},
		{Name: "old-launched", Stage: "launched", InstanceID: "own0000001", UpdatedAt: old},
	} {
		r.PoolID = "pool-a"
		records[r.Name] = r
	}
	if err := os.MkdirAll(p.statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := p.saveState(records); err != nil {
		t.Fatal(err)
	}
	got, err := p.loadState()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"old-deleted", "old-failed"} {
		if _, ok := got[name]; ok {
			t.Fatalf("%s survived compaction", name)
		}
	}
	for _, name := range []string{"recent-deleted", "old-pending", "old-unknown", "old-launched"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("%s was compacted", name)
		}
	}
}

func TestRecentlyRetiredNameStillCannotBeReused(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	ctx := context.Background()
	if _, err := p.CreateInstance(ctx, testParams("runner-a")); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteInstance(ctx, "runner-a"); err != nil {
		t.Fatal(err)
	}
	_, err := p.CreateInstance(ctx, testParams("runner-a"))
	if err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("retired name not refused distinctly: %v", err)
	}
	if api.creates != 1 {
		t.Fatal("retired name allocated another VM")
	}
}

func TestSweepRemovesOnlyUnheldRunnerLocks(t *testing.T) {
	p := testProvider(t, newFake())
	ctx := context.Background()
	held, err := p.lockFile(ctx, runnerLockPrefix+"held.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	free, err := p.lockFile(ctx, runnerLockPrefix+"free.lock")
	if err != nil {
		t.Fatal(err)
	}
	free()
	journal, err := p.lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	journal()
	p.sweepRunnerLocks()
	if got := runnerLocks(t, p); len(got) != 1 || got[0] != runnerLockPrefix+"held.lock" {
		t.Fatalf("sweep left %v", got)
	}
	if _, err := os.Stat(filepath.Join(p.statePath, "lock")); err != nil {
		t.Fatalf("journal lock removed: %v", err)
	}
}

func TestLockFollowsReplacedFile(t *testing.T) {
	p := testProvider(t, newFake())
	ctx := context.Background()
	name := runnerLockPrefix + "a.lock"
	first, err := p.lockFile(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	// Each open by the waiter is reported here. The first report proves it holds a
	// descriptor for the original inode before that inode is unlinked.
	opened := make(chan struct{}, 2)
	p.afterLockOpen = func(string) { opened <- struct{}{} }
	acquired := make(chan func(), 1)
	go func() {
		unlock, err := p.lockFile(ctx, name)
		if err != nil {
			t.Error(err)
			unlock = func() {}
		}
		acquired <- unlock
	}()
	<-opened
	if err := os.Remove(filepath.Join(p.statePath, name)); err != nil {
		t.Fatal(err)
	}
	first()
	second := <-acquired
	defer second()
	p.afterLockOpen = nil
	if len(opened) != 1 {
		t.Fatalf("waiter opened the lock %d more time(s); want exactly one reopen after locking the unlinked inode", len(opened))
	}
	short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if unlock, err := p.lockFile(short, name); err == nil {
		unlock()
		t.Fatal("waiter kept a lock on an unlinked file; a new caller was not excluded")
	}
}

func TestDeleteRemovesRunnerLocks(t *testing.T) {
	p := testProvider(t, newFake())
	ctx := context.Background()
	if _, err := p.CreateInstance(ctx, testParams("runner-a")); err != nil {
		t.Fatal(err)
	}
	if len(runnerLocks(t, p)) != 1 {
		t.Fatal("expected a runner lock after create")
	}
	if err := p.DeleteInstance(ctx, "runner-a"); err != nil {
		t.Fatal(err)
	}
	if got := runnerLocks(t, p); len(got) != 0 {
		t.Fatalf("runner locks remain: %v", got)
	}
}

func TestDeleteReleasesJournalLockDuringCloudCalls(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	ctx := context.Background()
	if _, err := p.CreateInstance(ctx, testParams("runner-a")); err != nil {
		t.Fatal(err)
	}
	api.deleteStarted = make(chan struct{})
	api.deleteContinue = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- p.DeleteInstance(ctx, "runner-a") }()
	<-api.deleteStarted
	short, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	unlock, err := p.lock(short)
	if err != nil {
		t.Fatalf("journal lock held across cloud DELETE: %v", err)
	}
	unlock()
	close(api.deleteContinue)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if stage, _ := p.loadState(); stage["runner-a"].Stage != "deleted" {
		t.Fatalf("record not retired: %+v", stage["runner-a"])
	}
}

func TestDeleteAbortsWhenCreationAdvances(t *testing.T) {
	api := newFake()
	p := testProvider(t, api)
	ctx := context.Background()
	if err := os.MkdirAll(p.statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	pending := map[string]creation{"runner-a": {Name: "runner-a", PoolID: "pool-a", Stage: "allocated", InstanceID: "gone000001", UpdatedAt: time.Now().UTC()}}
	if err := p.saveState(pending); err != nil {
		t.Fatal(err)
	}
	// The VM is already gone, so the old logic would retire the record. A concurrent
	// create re-allocating under this name must not be retired by a stale decision.
	p.beforeDeleteJournal = func() {
		records, _ := p.loadState()
		r := records["runner-a"]
		r.InstanceID, r.Stage = "late000001", "allocated"
		records["runner-a"] = r
		if err := p.saveState(records); err != nil {
			t.Error(err)
		}
	}
	if err := p.DeleteInstance(ctx, "runner-a"); err == nil || !strings.Contains(err.Error(), "advanced") {
		t.Fatalf("stale delete decision applied: %v", err)
	}
	if records, _ := p.loadState(); records["runner-a"].Stage != "allocated" {
		t.Fatalf("advanced creation retired: %+v", records["runner-a"])
	}
	p.beforeDeleteJournal = nil
	if err := p.DeleteInstance(ctx, "runner-a"); err != nil {
		t.Fatalf("retry against current inventory failed: %v", err)
	}
}

func TestJournalSurvivesExhaustedCreateBudget(t *testing.T) {
	p := testProvider(t, newFake())
	if err := os.MkdirAll(p.statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := p.saveState(map[string]creation{"runner-a": {Name: "runner-a", PoolID: "pool-a", Stage: "allocated", InstanceID: "own0000001", UpdatedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := p.record(expired, "runner-a", "own0000001", "launching"); err != nil {
		t.Fatalf("bookkeeping failed after the cloud budget expired: %v", err)
	}
	if stage, err := p.creationStage(expired, "runner-a"); err != nil || stage != "launching" {
		t.Fatalf("stage read failed: %q %v", stage, err)
	}
}
