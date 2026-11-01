// Copyright 2026 The garm-provider-agent37 authors
// SPDX-License-Identifier: Apache-2.0

package client

import "encoding/json"

type Resources struct {
	CPU    int `json:"cpu"`
	Memory int `json:"memory"`
	Disk   int `json:"disk"`
}

type CreateRequest struct {
	Name      string            `json:"name"`
	Template  string            `json:"template"`
	Type      string            `json:"type"`
	Region    string            `json:"region"`
	MountPath string            `json:"mount_path"`
	Resources Resources         `json:"resources"`
	AutoSleep bool              `json:"auto_sleep"`
	Metadata  map[string]string `json:"metadata"`
}

type StatusReason struct {
	Code      string `json:"code"`
	Operation string `json:"operation"`
	At        int64  `json:"at"`
}

type Instance struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Status           string            `json:"status"`
	StatusReason     *StatusReason     `json:"status_reason"`
	Type             string            `json:"type"`
	Template         string            `json:"template"`
	TemplateRevision int               `json:"template_revision"`
	Region           string            `json:"region"`
	MountPath        string            `json:"mount_path"`
	Resources        Resources         `json:"resources"`
	Metadata         map[string]string `json:"metadata"`
	AutoSleep        bool              `json:"auto_sleep"`
	PastDue          bool              `json:"past_due"`
}

func (i *Instance) UnmarshalJSON(data []byte) error {
	type instance Instance
	var value struct {
		*instance
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	var decoded Instance
	value.instance = (*instance)(&decoded)
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	decoded.Metadata = make(map[string]string, len(value.Metadata))
	for key, raw := range value.Metadata {
		var str string
		if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &str) == nil {
			decoded.Metadata[key] = str
		}
	}
	if decoded.StatusReason != nil {
		decoded.StatusReason.Code = safeCode(decoded.StatusReason.Code)
	}
	*i = decoded
	return nil
}

type ExecResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
}
