// Copyright 2026 The garm-provider-agent37 Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/cloudbase/garm-provider-common/params"
)

//go:embed launcher.py
var launcher string

const ReadyCommand = "test -f /root/garm-image/ready.json && test -x /usr/local/bin/gitea-runner && /usr/local/bin/gitea-runner --version >/dev/null 2>&1 && docker info >/dev/null 2>&1"
const ResumeCommand = "python3 /root/garm/launcher.py resume"

func Command(p params.BootstrapInstance) (string, error) {
	if p.Name == "" || p.InstanceToken == "" || strings.ContainsAny(p.InstanceToken, "\x00\r\n") {
		return "", fmt.Errorf("Command: runner name and instance token are required")
	}
	if p.OSType != params.Linux || p.OSArch != params.Amd64 {
		return "", fmt.Errorf("Command: only linux/amd64 is supported")
	}
	if p.JitConfigEnabled || p.GitHubRunnerGroup != "" || len(p.SSHKeys) != 0 || p.UserDataOptions.EnableBootDebug || len(p.UserDataOptions.ExtraPackages) != 0 {
		return "", fmt.Errorf("Command: JIT, runner groups, SSH key injection, boot debugging and extra packages are unsupported")
	}
	for _, value := range []string{p.MetadataURL, p.CallbackURL} {
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", fmt.Errorf("Command: invalid metadata or callback URL")
		}
	}
	cfg := struct {
		Name        string             `json:"name"`
		MetadataURL string             `json:"metadata_url"`
		CallbackURL string             `json:"callback_url"`
		Token       string             `json:"token"`
		CABundle    string             `json:"ca_bundle"`
		Proxy       params.ProxyConfig `json:"proxy"`
	}{p.Name, p.MetadataURL, p.CallbackURL, p.InstanceToken, string(p.CACertBundle), p.ProxyConfig}
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("Command: encode configuration: %w", err)
	}
	code := "import base64,json\nsource=base64.b64decode('" + base64.StdEncoding.EncodeToString([]byte(launcher)) + "').decode()\nnamespace={'__name__':'garm_launcher'}\nexec(source,namespace)\ntry:\n namespace['launch'](json.loads(base64.b64decode('" + base64.StdEncoding.EncodeToString(data) + "')),source)\nexcept Exception:\n raise SystemExit('bootstrap rejected; replace worker or inspect private status')\n"
	command := "python3 - <<'GARM_BOOTSTRAP'\n" + code + "GARM_BOOTSTRAP\n"
	if len(command) > 96*1024 {
		return "", fmt.Errorf("Command: bootstrap payload too large or invalid")
	}
	return command, nil
}
