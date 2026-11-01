// Copyright 2026 The garm-provider-agent37 authors
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultControlURL = "https://api.agent37.com"
	DefaultExecURL    = "https://sandbox.agent37.app"
	maxRequestBytes   = 2 * 1024 * 1024
	maxResponseBytes  = 8 * 1024 * 1024
)

var instanceID = regexp.MustCompile(`^[a-z0-9]{10}$`)

type Client struct {
	key        string
	controlURL string
	execURL    string
	http       *http.Client
}

type APIError struct {
	HTTPStatus int
	Code       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Agent37 HTTP %d: %s", e.HTTPStatus, safeCode(e.Code))
}

type transportError struct{ cause error }

func (e *transportError) Error() string { return "request: HTTP transport failed" }
func (e *transportError) Unwrap() error { return e.cause }

func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.HTTPStatus == http.StatusNotFound
}

// IsRetryable describes transient errors, not whether repeating a mutation is safe.
func IsRetryable(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var transport *transportError
	if errors.As(err, &transport) {
		return true
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.HTTPStatus == http.StatusRequestTimeout || apiErr.HTTPStatus == http.StatusTooManyRequests ||
		apiErr.HTTPStatus >= 500 || (apiErr.HTTPStatus == http.StatusConflict &&
		(apiErr.Code == "try_again" || apiErr.Code == "capacity_unavailable"))
}

func New(apiKey, controlURL, execURL string, httpClient *http.Client) (*Client, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" || strings.ContainsAny(apiKey, "\r\n\t ") {
		return nil, errors.New("New: a nonempty API key without whitespace is required")
	}
	if controlURL == "" {
		controlURL = DefaultControlURL
	}
	if execURL == "" {
		execURL = DefaultExecURL
	}
	control, err := validateOrigin(controlURL, httpClient != nil)
	if err != nil {
		return nil, fmt.Errorf("New: invalid control origin: %w", err)
	}
	execution, err := validateOrigin(execURL, httpClient != nil)
	if err != nil {
		return nil, fmt.Errorf("New: invalid execution origin: %w", err)
	}
	var clone http.Client
	if httpClient != nil {
		clone = *httpClient
	}
	if clone.Timeout <= 0 {
		clone.Timeout = 90 * time.Second
	}
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	clone.Jar = nil
	return &Client{key: apiKey, controlURL: control, execURL: execution, http: &clone}, nil
}

func validateOrigin(raw string, allowLocalHTTP bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("validateOrigin: malformed URL")
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return "", errors.New("validateOrigin: expected an origin without credentials, path, query, or fragment")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
		if u.Scheme != "http" || !allowLocalHTTP || !loopback {
			return "", errors.New("validateOrigin: HTTPS is required except for injected loopback test clients")
		}
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

func (c *Client) Create(ctx context.Context, body CreateRequest) (Instance, error) {
	var result Instance
	if err := c.request(ctx, http.MethodPost, "/v1/instances", body, &result, false); err != nil {
		return Instance{}, fmt.Errorf("Create: %w", err)
	}
	if !instanceID.MatchString(result.ID) {
		return Instance{}, errors.New("Create: response is missing a valid instance ID; reconcile inventory before retrying")
	}
	return result, nil
}

func (c *Client) List(ctx context.Context) ([]Instance, error) {
	var result struct {
		Data       *[]Instance     `json:"data"`
		HasMore    bool            `json:"has_more"`
		NextCursor json.RawMessage `json:"next_cursor"`
		Next       json.RawMessage `json:"next"`
	}
	if err := c.request(ctx, http.MethodGet, "/v1/instances", nil, &result, false); err != nil {
		return nil, fmt.Errorf("List: %w", err)
	}
	if result.Data == nil {
		return nil, errors.New("List: response is missing inventory")
	}
	// The documented endpoint is unpaginated; partial inventory is unsafe for reconciliation.
	if result.HasMore || nonemptyCursor(result.NextCursor) || nonemptyCursor(result.Next) {
		return nil, errors.New("List: unsupported pagination; refusing partial inventory")
	}
	for _, value := range *result.Data {
		if !instanceID.MatchString(value.ID) {
			return nil, errors.New("List: inventory contains an invalid instance ID")
		}
	}
	return *result.Data, nil
}

func nonemptyCursor(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null" && string(raw) != `""`
}

func (c *Client) Get(ctx context.Context, id string) (Instance, error) {
	if !instanceID.MatchString(id) {
		return Instance{}, errors.New("Get: invalid instance ID")
	}
	var result Instance
	if err := c.request(ctx, http.MethodGet, "/v1/instances/"+id, nil, &result, false); err != nil {
		return Instance{}, fmt.Errorf("Get: %w", err)
	}
	if result.ID != id {
		return Instance{}, errors.New("Get: response instance ID does not match request")
	}
	return result, nil
}

func (c *Client) Delete(ctx context.Context, id string) error {
	if !instanceID.MatchString(id) {
		return errors.New("Delete: invalid instance ID")
	}
	var result struct {
		ID      string `json:"id"`
		Deleted bool   `json:"deleted"`
	}
	if err := c.request(ctx, http.MethodDelete, "/v1/instances/"+id, nil, &result, false); err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("Delete: %w", err)
	}
	if result.ID != id || !result.Deleted {
		return errors.New("Delete: response did not confirm deletion")
	}
	return nil
}

func (c *Client) Start(ctx context.Context, id string) error {
	if err := c.lifecycle(ctx, id, "start"); err != nil {
		return fmt.Errorf("Start: %w", err)
	}
	return nil
}

func (c *Client) Stop(ctx context.Context, id string) error {
	if err := c.lifecycle(ctx, id, "stop"); err != nil {
		return fmt.Errorf("Stop: %w", err)
	}
	return nil
}

func (c *Client) lifecycle(ctx context.Context, id, action string) error {
	if !instanceID.MatchString(id) {
		return errors.New("lifecycle: invalid instance ID")
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := c.request(ctx, http.MethodPost, "/v1/instances/"+id+"/"+action, nil, &result, false); err != nil {
		return fmt.Errorf("lifecycle: %w", err)
	}
	if result.ID != id {
		return errors.New("lifecycle: response instance ID does not match request")
	}
	return nil
}

func (c *Client) Exec(ctx context.Context, id, command, name string) (ExecResult, error) {
	if !instanceID.MatchString(id) {
		return ExecResult{}, errors.New("Exec: invalid instance ID")
	}
	if strings.TrimSpace(command) == "" || strings.ContainsRune(command, 0) {
		return ExecResult{}, errors.New("Exec: invalid command")
	}
	if len(name) > 128 || strings.ContainsAny(name, "\x00\r\n") {
		return ExecResult{}, errors.New("Exec: invalid run name")
	}
	body := struct {
		Command string `json:"command"`
		User    string `json:"user"`
		Name    string `json:"name,omitempty"`
	}{command, "root", name}
	var result struct {
		ExitCode  *int   `json:"exit_code"`
		Stdout    string `json:"stdout"`
		Stderr    string `json:"stderr"`
		Truncated bool   `json:"truncated"`
	}
	if err := c.request(ctx, http.MethodPost, "/v1/instances/"+id+"/exec", body, &result, true); err != nil {
		return ExecResult{}, fmt.Errorf("Exec: %w", err)
	}
	if result.ExitCode == nil {
		return ExecResult{}, errors.New("Exec: response is missing exit_code")
	}
	return ExecResult{ExitCode: *result.ExitCode, Stdout: result.Stdout, Stderr: result.Stderr, Truncated: result.Truncated}, nil
}

func (c *Client) request(ctx context.Context, method, path string, body, result any, execution bool) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return errors.New("request: cannot encode body")
		}
		if len(data) > maxRequestBytes {
			return errors.New("request: body exceeds API limit")
		}
	}
	origin := c.controlURL
	if execution {
		origin = c.execURL
	}
	req, err := http.NewRequestWithContext(ctx, method, origin+path, bytes.NewReader(data))
	if err != nil {
		return errors.New("request: cannot construct request")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if execution {
		req.Header.Set("X-Agent37-Key", c.key)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &transportError{cause: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return &transportError{cause: err}
	}
	if len(raw) > maxResponseBytes {
		return errors.New("request: response exceeds size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{HTTPStatus: resp.StatusCode, Code: responseCode(raw), RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return errors.New("request: invalid JSON response")
	}
	return nil
}

func responseCode(raw []byte) string {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return "unknown_error"
	}
	var code string
	if json.Unmarshal(envelope.Error, &code) != nil {
		var detail struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(envelope.Error, &detail) != nil {
			return "unknown_error"
		}
		code = detail.Code
	}
	return safeCode(code)
}

func safeCode(code string) string {
	switch code {
	case "invalid_api_key", "ip_not_allowed", "invalid_request", "forbidden", "not_found",
		"insufficient_balance", "instance_suspended", "instance_limit_reached", "tier_limit",
		"capacity_unavailable", "try_again", "rate_limited", "no_capacity", "provisioning_failed",
		"wake_failed", "wake_timeout", "container_unavailable", "internal_error", "unauthorized",
		"payload_too_large", "image_too_large", "image_ingest_failed":
		return code
	default:
		return "unknown_error"
	}
}

func retryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(value)
	if err == nil && seconds >= 0 && seconds <= 3600 {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		duration := time.Until(date)
		if duration > 0 && duration <= time.Hour {
			return duration
		}
	}
	return 0
}
