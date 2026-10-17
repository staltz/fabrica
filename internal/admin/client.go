package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/staltz/fabrica/internal/runs"
)

// Client reads run history from a Fabrica server's admin API, for
// `fabrica runs --server`. It sends the admin token in an Authorization
// header, and never in the URL.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient makes a client for the server at the address its factories
// use as `server:`, like https://fabrica.acme.dev, with that server's
// admin token.
func NewClient(server, adminToken string) *Client {
	return &Client{
		base:  strings.TrimRight(server, "/"),
		token: adminToken,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Error is an answer from the server that says no.
type Error struct {
	Status  int    // the HTTP status
	Message string // what the server said, or else the status text
}

func (e *Error) Error() string { return e.Message }

// parallelDetails is the most runs Details reads at the same time, so a big
// export doesn't hammer the server.
const parallelDetails = 4

// maxAnswer is the biggest answer the client reads. Runs are small; this
// only keeps a wrong address from filling memory.
const maxAnswer = 64 << 20

// longAgo is what stands for "no limit" when a filter has no Since: the
// server would otherwise look back 7 days.
const longAgo = "1970-01-01T00:00:00Z"

// List reads one page of runs, newest first, like (*runs.DB).List does. The
// zero Since means every run, not just the last 7 days the API assumes.
// runs.ListAll follows next across pages.
func (c *Client) List(ctx context.Context, f runs.Filter) ([]runs.Summary, string, error) {
	q := url.Values{}
	for name, value := range map[string]string{"factory": f.Factory, "routine": f.Routine, "repo": f.Repo, "runtime": f.Runtime, "before": f.Before} {
		if value != "" {
			q.Set(name, value)
		}
	}
	if len(f.Outcomes) > 0 {
		q.Set("outcome", strings.Join(f.Outcomes, ","))
	}
	q.Set("since", longAgo)
	if !f.Since.IsZero() {
		q.Set("since", f.Since.UTC().Format(time.RFC3339))
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	var page runs.Page
	if err := c.get(ctx, "/_admin/runs?"+q.Encode(), &page); err != nil {
		return nil, "", err
	}
	return page.Runs, page.Next, nil
}

// Detail reads one run in full. runID is the whole ID.
func (c *Client) Detail(ctx context.Context, runID string) (*runs.Detail, error) {
	var d runs.Detail
	if err := c.get(ctx, "/_admin/runs/"+url.PathEscape(runID), &d); err != nil {
		return nil, err
	}
	if d.Timeline == nil {
		d.Timeline = []runs.Event{}
	}
	return &d, nil
}

// Details reads several runs in full, at most four at a time, and returns
// them in the order of runIDs. It stops at the first error.
func (c *Client) Details(ctx context.Context, runIDs []string) ([]*runs.Detail, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make([]*runs.Detail, len(runIDs))
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		slots    = make(chan struct{}, parallelDetails)
	)
	for i, id := range runIDs {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			d, err := c.Detail(ctx, id)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil && firstErr == nil:
				firstErr = err
				cancel() // the others needn't finish
			case err == nil:
				out[i] = d
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// get reads a path of the admin API into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxAnswer))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		var said struct {
			Error string `json:"error"`
		}
		msg := http.StatusText(res.StatusCode)
		if json.Unmarshal(body, &said) == nil && said.Error != "" {
			msg = said.Error
		}
		return &Error{Status: res.StatusCode, Message: msg}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return errors.New("the server's answer isn't what a Fabrica admin API sends; is the address right?")
	}
	return nil
}

// IsStatus reports whether err is an answer from the server with that
// status.
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}
