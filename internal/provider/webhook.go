package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WebhookCall is one call to the webhook a runtime made for a stub (how it
// installs a pace trigger), which starts a run of the stub.
type WebhookCall struct {
	// Runtime is the stub's runtime, which says how to call its webhook.
	Runtime string
	URL     string
	// Token is what the runtime checks on each call: a bearer token, or
	// the value of Header. It's a secret, so it never goes into an error or
	// a log.
	Token string
	// Header is the request header Token goes in as it is, like
	// Authorization for "Bearer …". Empty sends "Authorization: Bearer
	// <Token>".
	Header string
	// Text is what the run is told about why it started.
	Text string
}

// webhookHeaders are the extra headers some runtimes want on each call.
var webhookHeaders = map[string]map[string]string{
	// Claude routines' fire endpoint
	// (POST /v1/claude_code/routines/<id>/fire) takes any API version from
	// 2023-06-01 on, with or without the routines beta header.
	"claude-code-cloud": {
		"anthropic-version": "2023-06-01",
		"anthropic-beta":    "experimental-cc-routine-2026-04-01",
	},
}

const (
	// webhookMaxAttempts bounds the tries of one call. Only answers that
	// say the runtime didn't start a run (429 and 503) are retried: a
	// webhook has no idempotency key, so retrying anything else could start
	// a second run.
	webhookMaxAttempts = 3
	webhookMaxWait     = 30 * time.Second
)

// FireWebhook calls a stub's webhook: a POST with its credential (the auth
// header the runtime gave, or a bearer token) and a JSON body, {"text": …}, which Claude routines pass to the
// session they start, and other runtimes accept as the webhook's payload.
// A 2xx answer means the runtime started the run.
func FireWebhook(ctx context.Context, client *http.Client, call WebhookCall) error {
	if client == nil {
		client = &http.Client{Timeout: time.Minute}
	}
	body, err := json.Marshal(map[string]string{"text": call.Text})
	if err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, call.URL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("the webhook URL doesn't work: %w", err)
		}
		if call.Header != "" {
			req.Header.Set(call.Header, call.Token)
		} else {
			req.Header.Set("Authorization", "Bearer "+call.Token)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "fabrica")
		for k, v := range webhookHeaders[call.Runtime] {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			var uerr *url.Error
			if errors.As(err, &uerr) {
				err = uerr.Err // drop the URL, which can hold a secret
			}
			return fmt.Errorf("calling the webhook: %w", err)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			return nil
		}
		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) && attempt < webhookMaxAttempts {
			if wait := retryDelay(resp.Header, attempt, time.Now()); wait <= webhookMaxWait {
				if err := sleep(ctx, wait); err != nil {
					return err
				}
				continue
			}
		}
		return fmt.Errorf("the webhook answered %s%s", resp.Status, webhookErrorDetail(data))
	}
}

// webhookErrorDetail is the useful part of an error answer, if there's
// one: the message of a JSON error, or a short plain text.
func webhookErrorDetail(data []byte) string {
	var e struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != nil {
		switch v := e.Error.(type) {
		case string:
			return ": " + v
		case map[string]any:
			if msg, ok := v["message"].(string); ok {
				return ": " + msg
			}
		}
	}
	text := strings.TrimSpace(string(data))
	if text == "" || len(text) > 200 || strings.ContainsAny(text, "<{") {
		return ""
	}
	return ": " + text
}
