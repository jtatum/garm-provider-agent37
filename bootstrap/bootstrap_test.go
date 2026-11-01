// Copyright 2026 The garm-provider-agent37 Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/cloudbase/garm-provider-common/params"
)

func validParams() params.BootstrapInstance {
	return params.BootstrapInstance{Name: "garm-test", InstanceToken: "test-instance-token", MetadataURL: "http://127.0.0.1/metadata", CallbackURL: "http://127.0.0.1/callback", OSType: params.Linux, OSArch: params.Amd64}
}

func TestCommandRejectsUnsupportedBootstrap(t *testing.T) {
	cases := map[string]func(*params.BootstrapInstance){
		"missing token":   func(p *params.BootstrapInstance) { p.InstanceToken = "" },
		"windows":         func(p *params.BootstrapInstance) { p.OSType = params.Windows },
		"arm":             func(p *params.BootstrapInstance) { p.OSArch = params.Arm64 },
		"JIT":             func(p *params.BootstrapInstance) { p.JitConfigEnabled = true },
		"SSH":             func(p *params.BootstrapInstance) { p.SSHKeys = []string{"public-key"} },
		"debug":           func(p *params.BootstrapInstance) { p.UserDataOptions.EnableBootDebug = true },
		"packages":        func(p *params.BootstrapInstance) { p.UserDataOptions.ExtraPackages = []string{"x"} },
		"URL credentials": func(p *params.BootstrapInstance) { p.MetadataURL = "https://user:secret@example.com/metadata" },
		"URL fragment":    func(p *params.BootstrapInstance) { p.MetadataURL += "#secret" },
		"oversize":        func(p *params.BootstrapInstance) { p.InstanceToken = strings.Repeat("x", 100*1024) },
	}
	for name, alter := range cases {
		t.Run(name, func(t *testing.T) {
			p := validParams()
			alter(&p)
			_, err := Command(p)
			if err == nil {
				t.Fatal("accepted invalid bootstrap")
			}
			if strings.Contains(err.Error(), p.InstanceToken) && p.InstanceToken != "" {
				t.Fatal("token leaked in error")
			}
		})
	}
}

func TestCommandKeepsValuesOutOfShellSyntax(t *testing.T) {
	p := validParams()
	p.Name = "name'; touch /tmp/should-not-exist; #"
	p.InstanceToken = "secret\"$(false)'"
	command, err := Command(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{p.Name, p.InstanceToken, p.MetadataURL, p.CallbackURL} {
		if strings.Contains(command, value) {
			t.Fatalf("raw value appears in command: %q", value)
		}
	}
	if len(command) >= 96*1024 {
		t.Fatal("command exceeds argv envelope")
	}
}

func TestRuntimeAndRenderedTemplate(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for bootstrap runtime tests")
	}
	raw, err := os.ReadFile("../templates/gitea-agent37.py.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := template.New("gitea").Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	context := map[string]any{"MetadataURL": "http://metadata.invalid", "CallbackURL": "http://callback.invalid", "CallbackToken": "instance-token", "RepoURL": "http://gitea.invalid", "RunnerName": "runner-'\"\\test", "RunnerLabels": "agent37-linux,runner-pool-id=pool-id", "ExtraContext": map[string]string{"agent37_container_network": "host"}, "AgentMode": false, "UseJITConfig": false}
	var rendered bytes.Buffer
	if err := tpl.Execute(&rendered, context); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rendered.py")
	if err := os.WriteFile(path, rendered.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, "test_runtime.py", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("runtime contract failed: %v\n%s", err, output)
	}
}

func TestReadyCommandRequiresImageReadiness(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "ready.json")
	runner := filepath.Join(directory, "gitea-runner")
	docker := filepath.Join(directory, "docker")
	for _, path := range []string{runner, docker} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	command := strings.ReplaceAll(ReadyCommand, "/root/garm-image/ready.json", marker)
	command = strings.ReplaceAll(command, "/usr/local/bin/gitea-runner", runner)
	check := func() error {
		cmd := exec.Command("sh", "-c", command)
		cmd.Env = append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH"))
		return cmd.Run()
	}
	if err := check(); err == nil {
		t.Fatal("accepted Docker before image preparation completed")
	}
	if err := os.WriteFile(marker, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := check(); err != nil {
		t.Fatalf("prepared image was not ready: %v", err)
	}
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := check(); err == nil {
		t.Fatal("accepted a stale marker with Docker unavailable")
	}
}
