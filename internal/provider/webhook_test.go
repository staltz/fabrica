package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFireWebhook(t *testing.T) {
	var calls atomic.Int32
	var answer atomic.Int32
	var got http.Header
	var body string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		got = r.Header.Clone()
		b := make([]byte, 512)
		k, _ := r.Body.Read(b)
		body = string(b[:k])
		switch answer.Load() {
		case 429:
			if n == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
		case 401:
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid token"}}`))
			return
		case 500:
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"type":"routine_fire","claude_code_session_id":"session_1"}`))
	}))
	defer ts.Close()
	ctx := context.Background()

	// Claude routines get their API's headers.
	call := WebhookCall{Runtime: "claude-code-cloud", URL: ts.URL + "/v1/claude_code/routines/trig_1/fire", Token: "sk-ant-oat01-secret", Text: "Fabrica's heartbeat"}
	if err := FireWebhook(ctx, nil, call); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "Bearer sk-ant-oat01-secret" || got.Get("anthropic-version") != "2023-06-01" || got.Get("anthropic-beta") != "experimental-cc-routine-2026-04-01" || got.Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v", got)
	}
	if body != `{"text":"Fabrica's heartbeat"}` {
		t.Errorf("body = %s", body)
	}

	// Other runtimes get a plain bearer call.
	call.Runtime = "cursor-cloud"
	if err := FireWebhook(ctx, nil, call); err != nil || got.Get("anthropic-version") != "" {
		t.Errorf("Cursor: %v, %v", err, got)
	}

	// A 429 is retried: the runtime started nothing.
	calls.Store(0)
	answer.Store(429)
	if err := FireWebhook(ctx, nil, call); err != nil || calls.Load() != 2 {
		t.Errorf("after a 429: %v, %d calls", err, calls.Load())
	}

	// Other failures aren't: a webhook has no idempotency key, so a retry
	// could start a second run. Errors never show the token.
	for code, want := range map[int32]string{401: "401 Unauthorized: invalid token", 500: "500 Internal Server Error"} {
		calls.Store(0)
		answer.Store(code)
		err := FireWebhook(ctx, nil, call)
		if err == nil || !strings.Contains(err.Error(), want) || calls.Load() != 1 || strings.Contains(err.Error(), "secret") {
			t.Errorf("answer %d: %v, %d calls", code, err, calls.Load())
		}
	}

	// A webhook that can't be reached says so, without its URL (which can
	// hold a secret of its own).
	call.URL = "http://127.0.0.1:1/hook?key=secret"
	if err := FireWebhook(ctx, nil, call); err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "calling the webhook") {
		t.Errorf("unreachable: %v", err)
	}
}
