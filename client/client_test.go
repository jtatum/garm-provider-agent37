// Copyright 2026 The garm-provider-agent37 authors
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testID = "a1b2c3d4e5"

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cli, err := New("test-key", server.URL, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return cli
}

func TestLifecycleAndSeparateExecAuthentication(t *testing.T) {
	var creates, gets, lists, starts, stops, deletes, execs atomic.Int32
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("X-Agent37-Key") != "" {
			t.Error("incorrect control authentication")
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Error("missing JSON accept header")
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/instances":
			creates.Add(1)
			var raw map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Error(err)
			}
			if string(raw["auto_sleep"]) != "false" {
				t.Error("auto_sleep false must be explicit")
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"a1b2c3d4e5","name":"test runner","status":"running","type":"vm","resources":{"cpu":4,"memory":8,"disk":20},"metadata":{"garm-controller-id":"controller"}}`)
		case r.Method == "GET" && r.URL.Path == "/v1/instances":
			lists.Add(1)
			fmt.Fprint(w, `{"data":[{"id":"a1b2c3d4e5","name":null,"template_revision":null,"metadata":{"garm-controller-id":"controller","foreign-number":12,"foreign-object":{"value":true}},"status_reason":{"code":"provisioning_failed","message":"private runtime output","detail":"secret","operation":"start","at":1}}]}`)
		case r.Method == "GET" && r.URL.Path == "/v1/instances/"+testID:
			gets.Add(1)
			fmt.Fprint(w, `{"id":"a1b2c3d4e5","status":"running"}`)
		case r.Method == "POST" && r.URL.Path == "/v1/instances/"+testID+"/start":
			starts.Add(1)
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"id":"a1b2c3d4e5","status":"starting"}`)
		case r.Method == "POST" && r.URL.Path == "/v1/instances/"+testID+"/stop":
			stops.Add(1)
			fmt.Fprint(w, `{"id":"a1b2c3d4e5","status":"stopped"}`)
		case r.Method == "DELETE" && r.URL.Path == "/v1/instances/"+testID:
			if deletes.Add(1) == 1 {
				fmt.Fprint(w, `{"id":"a1b2c3d4e5","deleted":true}`)
			} else {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer control.Close()
	execution := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		execs.Add(1)
		if r.Header.Get("X-Agent37-Key") != "test-key" || r.Header.Get("Authorization") != "" {
			t.Error("incorrect exec authentication")
		}
		if r.Method != "POST" || r.URL.Path != "/v1/instances/"+testID+"/exec" {
			t.Error("incorrect exec route")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["command"] != "exit 17" || body["user"] != "root" || body["name"] != "probe-1" {
			t.Errorf("wrong exec body: %#v", body)
		}
		fmt.Fprint(w, `{"exit_code":17,"stdout":"result","stderr":"diagnostic","truncated":true}`)
	}))
	defer execution.Close()
	cli, err := New("test-key", control.URL, execution.URL, control.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	got, err := cli.Create(ctx, CreateRequest{Name: "test runner", Template: "runner@2", Type: "vm", Region: "us", MountPath: "/root", Resources: Resources{4, 8, 20}, Metadata: map[string]string{"garm-controller-id": "controller"}})
	if err != nil || got.ID != testID || got.Resources.CPU != 4 {
		t.Fatalf("Create got %#v, %v", got, err)
	}
	items, err := cli.List(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("List: %#v, %v", items, err)
	}
	if len(items[0].Metadata) != 1 || items[0].Metadata["garm-controller-id"] != "controller" {
		t.Fatalf("wrong metadata: %#v", items[0].Metadata)
	}
	if items[0].StatusReason == nil || items[0].StatusReason.Code != "provisioning_failed" {
		t.Fatal("missing structured status reason")
	}
	serialized, _ := json.Marshal(items[0])
	if strings.Contains(string(serialized), "private runtime output") || strings.Contains(string(serialized), "secret") {
		t.Fatal("kept raw status logs")
	}
	if _, err := cli.Get(ctx, testID); err != nil {
		t.Fatal(err)
	}
	if err := cli.Start(ctx, testID); err != nil {
		t.Fatal(err)
	}
	if err := cli.Stop(ctx, testID); err != nil {
		t.Fatal(err)
	}
	result, err := cli.Exec(ctx, testID, "exit 17", "probe-1")
	if err != nil || result.ExitCode != 17 || !result.Truncated || result.Stdout != "result" || result.Stderr != "diagnostic" {
		t.Fatalf("Exec: %#v, %v", result, err)
	}
	if err := cli.Delete(ctx, testID); err != nil {
		t.Fatal(err)
	}
	if err := cli.Delete(ctx, testID); err != nil {
		t.Fatal(err)
	}
	for name, counter := range map[string]*atomic.Int32{"create": &creates, "get": &gets, "list": &lists, "start": &starts, "stop": &stops, "exec": &execs} {
		if counter.Load() != 1 {
			t.Errorf("%s called %d times", name, counter.Load())
		}
	}
	if deletes.Load() != 2 {
		t.Fatal("wrong delete count")
	}
}

func TestErrorsAreRedactedAndClassified(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, code string
		retry      bool
	}{
		{"rate limit", 429, `{"error":{"code":"rate_limited","message":"secret-command","detail":"test-key"}}`, "rate_limited", true},
		{"flat error", 404, `{"error":"not_found"}`, "not_found", false},
		{"capacity", 409, `{"error":{"code":"capacity_unavailable"}}`, "capacity_unavailable", true},
		{"limit", 409, `{"error":{"code":"instance_limit_reached"}}`, "instance_limit_reached", false},
		{"unauthorized", 401, `{"error":{"code":"invalid_api_key"}}`, "invalid_api_key", false},
		{"unknown secret code", 500, `{"error":{"code":"sk_live_private","message":"secret-command"}}`, "unknown_error", true},
		{"non JSON", 502, "secret-command test-key", "unknown_error", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			_, err := cli.Exec(context.Background(), testID, "secret-command", "request-1")
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.HTTPStatus != tc.status || apiErr.Code != tc.code || apiErr.RetryAfter != 3*time.Second {
				t.Fatalf("error: %#v %v", apiErr, err)
			}
			if IsRetryable(err) != tc.retry || IsNotFound(err) != (tc.status == 404) {
				t.Fatal("wrong error classification")
			}
			for _, sensitive := range []string{"secret-command", "test-key", "sk_live_private"} {
				if strings.Contains(err.Error(), sensitive) {
					t.Fatal("error leaked response/request content")
				}
			}
		})
	}
}

func TestAmbiguousCreateIsNeverRetried(t *testing.T) {
	for _, response := range []string{"disconnect", "503", "malformed"} {
		t.Run(response, func(t *testing.T) {
			var calls atomic.Int32
			cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch response {
				case "disconnect":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
				case "503":
					w.WriteHeader(503)
					fmt.Fprint(w, `{"error":{"code":"no_capacity"}}`)
				default:
					fmt.Fprint(w, `{"id":`)
				}
			})
			if _, err := cli.Create(context.Background(), CreateRequest{Name: "unique"}); err == nil {
				t.Fatal("expected ambiguous error")
			}
			if calls.Load() != 1 {
				t.Fatalf("mutation retried %d times", calls.Load())
			}
		})
	}
}

func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/secret?token=test-key", http.StatusTemporaryRedirect)
	})
	_, err := cli.Get(context.Background(), testID)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 307 {
		t.Fatalf("unexpected redirect error: %v", err)
	}
	if targetCalls.Load() != 0 || strings.Contains(err.Error(), "test-key") {
		t.Fatal("redirect leaked credentials")
	}
}

func TestInventoryCannotSilentlyBecomePartial(t *testing.T) {
	for _, body := range []string{
		`{"data":[],"has_more":true}`, `{"data":[],"next_cursor":"page-2"}`, `{"data":[],"next":"https://elsewhere.example"}`, `{}`, `{"data":null}`, `{"data":[{"id":"../../outside"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if result, err := cli.List(context.Background()); err == nil || result != nil {
				t.Fatalf("partial inventory accepted: %#v, %v", result, err)
			}
		})
	}
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[],"has_more":false,"next_cursor":null}`)
	})
	if result, err := cli.List(context.Background()); err != nil || len(result) != 0 {
		t.Fatalf("empty inventory rejected: %v", err)
	}
}

func TestMissingAndMismatchedResultFields(t *testing.T) {
	for _, body := range []string{`{}`, `{"exit_code":null}`, `{"exit_code":"0"}`, `{"exit_code":0} secret-content`} {
		cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
		if _, err := cli.Exec(context.Background(), testID, "true", "check"); err == nil {
			t.Fatal("missing/bad exit code accepted")
		}
	}
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"id":"z9y8x7w6v5","deleted":true}`) })
	if _, err := cli.Get(context.Background(), testID); err == nil {
		t.Fatal("mismatched get ID accepted")
	}
	if err := cli.Delete(context.Background(), testID); err == nil {
		t.Fatal("mismatched delete ID accepted")
	}
	if err := cli.Start(context.Background(), testID); err == nil {
		t.Fatal("mismatched start ID accepted")
	}
}

func TestInvalidArgumentsMakeNoRequest(t *testing.T) {
	var calls atomic.Int32
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	ctx := context.Background()
	for _, id := range []string{"", "../../secrets", testID + "?x=y", "ABCDEF0123"} {
		if _, err := cli.Get(ctx, id); err == nil {
			t.Fatal("Get accepted invalid ID")
		}
		if _, err := cli.Exec(ctx, id, "true", ""); err == nil {
			t.Fatal("Exec accepted invalid ID")
		}
		if err := cli.Delete(ctx, id); err == nil {
			t.Fatal("Delete accepted invalid ID")
		}
		if err := cli.Start(ctx, id); err == nil {
			t.Fatal("Start accepted invalid ID")
		}
		if err := cli.Stop(ctx, id); err == nil {
			t.Fatal("Stop accepted invalid ID")
		}
	}
	for _, command := range []string{"", " ", "cmd\x00"} {
		if _, err := cli.Exec(ctx, testID, command, ""); err == nil {
			t.Fatal("invalid command accepted")
		}
	}
	if _, err := cli.Exec(ctx, testID, "true", strings.Repeat("a", 129)); err == nil {
		t.Fatal("long exec name accepted")
	}
	if _, err := cli.Exec(ctx, testID, strings.Repeat("x", maxRequestBytes), "large"); err == nil {
		t.Fatal("large request accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached network")
	}
}

func TestOriginAndCredentialValidation(t *testing.T) {
	for _, origin := range []string{"http://api.agent37.com", "https://user:secret@api.agent37.com", "https://api.agent37.com/path", "https://api.agent37.com?secret=value", "https://api.agent37.com#fragment", "file:///tmp/api"} {
		if _, err := New("test-key", origin, DefaultExecURL, &http.Client{}); err == nil {
			t.Errorf("accepted origin %s", origin)
		}
	}
	if _, err := New("test-key", "http://127.0.0.1:1", "", nil); err == nil {
		t.Fatal("non-injected HTTP accepted")
	}
	for _, key := range []string{"", " ", "bad\r\nkey", "bad key"} {
		if _, err := New(key, "", "", nil); err == nil {
			t.Fatal("bad key accepted")
		}
	}
	cli, err := New("test-key", "", "", nil)
	if err != nil || cli.controlURL != DefaultControlURL || cli.execURL != DefaultExecURL || cli.http.Timeout <= 0 {
		t.Fatal("bad defaults")
	}
}

func TestRequestCancellationAndTimeout(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			arrived := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(arrived); <-r.Context().Done() }))
			defer server.Close()
			h := server.Client()
			if mode == "timeout" {
				h.Timeout = 30 * time.Millisecond
			}
			cli, err := New("test-key", server.URL, server.URL, h)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := cli.Get(ctx, testID); result <- err }()
			select {
			case <-arrived:
			case <-time.After(time.Second):
				t.Fatal("request not received")
			}
			if mode == "cancel" {
				cancel()
			}
			select {
			case err := <-result:
				if mode == "cancel" && (!errors.Is(err, context.Canceled) || IsRetryable(err)) {
					t.Fatalf("cancellation lost: %v", err)
				}
				if mode == "timeout" && (!errors.Is(err, context.DeadlineExceeded) || !IsRetryable(err)) {
					t.Fatalf("timeout lost: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("request did not stop")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportAndDecodeErrorsDoNotLeakData(t *testing.T) {
	h := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: "https://sensitive?token=private-key", Err: errors.New("secret-command")}
	})}
	cli, err := New("test-key", "", "", h)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cli.Get(context.Background(), testID)
	if err == nil || strings.Contains(err.Error(), "private-key") || strings.Contains(err.Error(), "secret-command") {
		t.Fatalf("transport error leaked: %v", err)
	}
	cli = newTestClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"exit_code":"private-key"}`) })
	_, err = cli.Exec(context.Background(), testID, "secret-command", "")
	if err == nil || strings.Contains(err.Error(), "private-key") {
		t.Fatalf("decode error leaked: %v", err)
	}
}

func TestResponseSizeIsBounded(t *testing.T) {
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
	})
	if _, err := cli.List(context.Background()); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("large response accepted: %v", err)
	}
}
