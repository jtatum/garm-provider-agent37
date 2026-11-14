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
	"syscall"
	"time"
)

const (
	runnerLockPrefix = "runner-"
	// GARM retries a failed create under the same name within minutes and removes the
	// instance after a few attempts; a day of retired names comfortably covers that.
	retiredRetention = 24 * time.Hour
	journalTimeout   = 30 * time.Second
)

type creation struct {
	Name        string    `json:"name"`
	PoolID      string    `json:"pool_id"`
	Fingerprint string    `json:"fingerprint"`
	InstanceID  string    `json:"instance_id,omitempty"`
	Stage       string    `json:"stage"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (p *Provider) lock(ctx context.Context) (func(), error) {
	return p.lockFile(ctx, "lock")
}

func (p *Provider) lockFile(ctx context.Context, filename string) (func(), error) {
	if err := os.MkdirAll(p.statePath, 0o700); err != nil {
		return nil, fmt.Errorf("lockFile: create state directory: %w", err)
	}
	for _, path := range []string{p.cfg.StateDir, p.statePath} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("lockFile: state directories must be private directories (0700)")
		}
	}
	path := filepath.Join(p.statePath, filename)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return nil, fmt.Errorf("lockFile: open lock: %w", err)
		}
		if p.afterLockOpen != nil {
			p.afterLockOpen(filename)
		}
		if err := acquire(ctx, f); err != nil {
			f.Close()
			return nil, fmt.Errorf("lockFile: %w", err)
		}
		if sameFile(f, path) {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		// sweepRunnerLocks unlinked this file after it was opened. Holding a lock on the
		// orphaned inode would not exclude a caller that opened the replacement path.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}

func acquire(ctx context.Context, f *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("acquire lock: %w", err)
		}
		if err := wait(ctx, 25*time.Millisecond); err != nil {
			return err
		}
	}
}

func sameFile(f *os.File, path string) bool {
	held, err := f.Stat()
	if err != nil {
		return false
	}
	named, err := os.Lstat(path)
	return err == nil && os.SameFile(held, named)
}

// sweepRunnerLocks removes per-runner lock files that no invocation holds. A runner lock is
// held for the whole of CreateInstance, so an unheld file protects nothing; lockFile re-checks
// the inode after locking, so a caller racing with removal moves to the replacement file.
// The journal lock is never removed. Failures are ignored: leftover files are only clutter.
func (p *Provider) sweepRunnerLocks() {
	entries, err := os.ReadDir(p.statePath)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasPrefix(name, runnerLockPrefix) || !strings.HasSuffix(name, ".lock") {
			continue
		}
		path := filepath.Join(p.statePath, name)
		f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
		if err != nil {
			continue
		}
		if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			if sameFile(f, path) {
				_ = os.Remove(path)
			}
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		}
		f.Close()
	}
}

// journalContext bounds local journal bookkeeping separately from the caller's cloud budget.
// A worker that booted slowly may have little create time left; brief lock contention must not
// then discard it. Like failCreate cleanup, it does not inherit cancellation: once a cloud side
// effect has happened, recording it is what keeps later reconciliation and deletion safe.
func journalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), journalTimeout)
}

func (p *Provider) loadState() (map[string]creation, error) {
	data, err := os.ReadFile(filepath.Join(p.statePath, "creations.json"))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]creation{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loadState: read journal: %w", err)
	}
	var records map[string]creation
	if err := json.Unmarshal(data, &records); err != nil || records == nil {
		return nil, fmt.Errorf("loadState: invalid creation journal; refusing new capacity")
	}
	return records, nil
}

// compact drops retired records once GARM can no longer be retrying their names. They only
// block name reuse and restarts; keeping them forever made every journal operation slower.
// Ambiguous (pending/unknown) and active records are never dropped.
func compact(records map[string]creation, now time.Time) {
	for name, record := range records {
		if (record.Stage == "deleted" || record.Stage == "failed") && now.Sub(record.UpdatedAt) > retiredRetention {
			delete(records, name)
		}
	}
}

func (p *Provider) saveState(records map[string]creation) error {
	compact(records, time.Now().UTC())
	data, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("saveState: encode journal: %w", err)
	}
	f, err := os.CreateTemp(p.statePath, ".creations-*")
	if err != nil {
		return fmt.Errorf("saveState: create journal: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("saveState: write journal: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("saveState: sync journal: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("saveState: close journal: %w", err)
	}
	if err := os.Rename(f.Name(), filepath.Join(p.statePath, "creations.json")); err != nil {
		return fmt.Errorf("saveState: replace journal: %w", err)
	}
	dir, err := os.Open(p.statePath)
	if err != nil {
		return fmt.Errorf("saveState: open directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("saveState: sync directory: %w", err)
	}
	return nil
}

func (p *Provider) record(ctx context.Context, name, id, stage string) error {
	ctx, cancel := journalContext(ctx)
	defer cancel()
	unlock, err := p.lock(ctx)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}
	defer unlock()
	records, err := p.loadState()
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}
	r, ok := records[name]
	if !ok {
		return fmt.Errorf("record: creation reservation is missing")
	}
	if r.Stage == "deleted" && stage != "deleted" {
		return fmt.Errorf("record: retired creation cannot be restarted")
	}
	r.Stage, r.UpdatedAt = stage, time.Now().UTC()
	if id != "" {
		r.InstanceID = id
	}
	records[name] = r
	if err := p.saveState(records); err != nil {
		return fmt.Errorf("record: %w", err)
	}
	return nil
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p *Provider) creationStage(ctx context.Context, name string) (string, error) {
	ctx, cancel := journalContext(ctx)
	defer cancel()
	unlock, err := p.lock(ctx)
	if err != nil {
		return "", fmt.Errorf("creationStage: %w", err)
	}
	defer unlock()
	records, err := p.loadState()
	if err != nil {
		return "", fmt.Errorf("creationStage: %w", err)
	}
	record, ok := records[name]
	if !ok {
		return "", fmt.Errorf("creationStage: missing creation intent")
	}
	return record.Stage, nil
}
