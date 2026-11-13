// Package client implements a thin, hand-rolled HTTP client for the HubSpot
// REST API with proactive token-bucket rate limiting, policy-aware 429
// handling, retries for idempotent verbs, typed errors, and cursor-based
// pagination helpers.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// DebugLogger receives wire-level debug log events. It matches the signature
// of tflog.Debug so provider code can wire it directly:
//
//	client.DebugLogger = tflog.Debug
//
// It defaults to a no-op; the client never logs to stdout/stderr. (tflog is
// not imported here directly because github.com/hashicorp/go-hclog, its
// transitive dependency, is not yet present in go.sum.)
var DebugLogger func(ctx context.Context, msg string, additionalFields ...map[string]any)

func logDebug(ctx context.Context, msg string, fields map[string]any) {
	if DebugLogger != nil {
		DebugLogger(ctx, msg, fields)
	}
}

const (
	defaultMaxRetries     = 5
	defaultRequestsPer10s = 100
	defaultBurst          = 10
	defaultRetryWaitMin   = 250 * time.Millisecond
	defaultRetryWaitMax   = 10 * time.Second
	maxErrorBodyBytes     = 1 << 20 // 1 MiB
)

// Config configures a Client.
type Config struct {
	// BaseURL is the root URL of the HubSpot API, e.g. "https://api.hubapi.com".
	// Required.
	BaseURL string
	// AccessToken is the HubSpot private-app access token sent as a Bearer
	// token. Set exactly one of AccessToken and TokenSource.
	AccessToken string
	// TokenSource supplies the Bearer token for each request, for credentials
	// that expire during a run. Set exactly one of AccessToken and TokenSource.
	TokenSource TokenSource
	// UserAgent is sent as the User-Agent header on every request.
	UserAgent string
	// MaxRetries caps the number of retry attempts after the initial request.
	// Defaults to 5.
	MaxRetries int

	// RequestsPer10s tunes the proactive token-bucket rate limiter, expressed
	// as requests allowed per rolling 10 seconds. Defaults to 100.
	RequestsPer10s float64
	// HTTPClient overrides the underlying *http.Client. Defaults to
	// http.DefaultClient.
	HTTPClient *http.Client
	// RetryWaitMin is the initial backoff delay used when the server does not
	// supply a Retry-After header. Defaults to 250ms.
	RetryWaitMin time.Duration
	// RetryWaitMax caps the exponential backoff delay. Defaults to 10s.
	RetryWaitMax time.Duration
}

// TokenSource supplies a Bearer token. Token is called before every request
// attempt, so it should cache the token until shortly before it expires.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

type staticToken string

func (t staticToken) Token(context.Context) (string, error) { return string(t), nil }

// Client is a HubSpot API client. Create one with New. It is safe for
// concurrent use by multiple goroutines.
type Client struct {
	baseURL      string
	tokenSource  TokenSource
	userAgent    string
	maxRetries   int
	retryWaitMin time.Duration
	retryWaitMax time.Duration
	httpClient   *http.Client
	limiter      *rate.Limiter
}

// New validates cfg and returns a ready-to-use Client.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("hubspot client: Config.BaseURL is required")
	}
	tokenSource := cfg.TokenSource
	switch {
	case cfg.AccessToken != "" && tokenSource != nil:
		return nil, errors.New("hubspot client: set only one of Config.AccessToken and Config.TokenSource")
	case cfg.AccessToken != "":
		tokenSource = staticToken(cfg.AccessToken)
	case tokenSource == nil:
		return nil, errors.New("hubspot client: Config.AccessToken or Config.TokenSource is required")
	}
	if _, err := url.Parse(cfg.BaseURL); err != nil {
		return nil, fmt.Errorf("hubspot client: invalid BaseURL %q: %w", cfg.BaseURL, err)
	}

	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = defaultMaxRetries
	}
	per10s := cfg.RequestsPer10s
	if per10s <= 0 {
		per10s = defaultRequestsPer10s
	}
	waitMin := cfg.RetryWaitMin
	if waitMin <= 0 {
		waitMin = defaultRetryWaitMin
	}
	waitMax := cfg.RetryWaitMax
	if waitMax <= 0 {
		waitMax = defaultRetryWaitMax
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &Client{
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
		tokenSource:  tokenSource,
		userAgent:    cfg.UserAgent,
		maxRetries:   maxRetries,
		retryWaitMin: waitMin,
		retryWaitMax: waitMax,
		httpClient:   httpClient,
		limiter:      rate.NewLimiter(rate.Limit(per10s/10.0), defaultBurst),
	}, nil
}

// Get issues a GET request to path with the given query parameters and
// decodes a JSON response into out. Pass out == nil to discard the body.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

// Post issues a POST request to path with body JSON-encoded and decodes a
// JSON response into out. Pass out == nil to discard the body.
func (c *Client) Post(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPost, path, nil, body, out)
}

// Patch issues a PATCH request to path with body JSON-encoded and decodes a
// JSON response into out. Pass out == nil to discard the body.
func (c *Client) Patch(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPatch, path, nil, body, out)
}

// Put issues a PUT request to path with body JSON-encoded and decodes a JSON
// response into out. Pass out == nil to discard the body.
func (c *Client) Put(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPut, path, nil, body, out)
}

// Delete issues a DELETE request to path with the given query parameters.
func (c *Client) Delete(ctx context.Context, path string, query url.Values) error {
	return c.do(ctx, http.MethodDelete, path, query, nil, nil)
}

// isIdempotent reports whether method is safe to retry on transient 5xx
// errors. POST and PATCH are never retried.
func isIdempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodHead:
		return true
	}
	return false
}

// do runs the request/retry loop for a single logical API call.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.baseURL + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("hubspot client: encoding %s %s request body: %w", method, path, err)
		}
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return fmt.Errorf("hubspot client: rate limiter: %w", err)
		}

		var reqBody io.Reader
		if bodyBytes != nil {
			reqBody = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
		if err != nil {
			return fmt.Errorf("hubspot client: building %s %s request: %w", method, path, err)
		}
		token, err := c.tokenSource.Token(ctx)
		if err != nil {
			return fmt.Errorf("hubspot client: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if c.userAgent != "" {
			req.Header.Set("User-Agent", c.userAgent)
		}
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		logDebug(ctx, "hubspot API request", map[string]any{
			"method":  method,
			"url":     u,
			"attempt": attempt + 1,
		})

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// Transport-level error (connection refused, timeout, ...).
			lastErr = fmt.Errorf("hubspot client: %s %s: %w", method, path, err)
			if ctx.Err() != nil || !isIdempotent(method) || attempt == c.maxRetries {
				return lastErr
			}
			if err := c.sleepBackoff(ctx, attempt, ""); err != nil {
				return err
			}
			continue
		}

		done, err := c.handleResponse(ctx, resp, method, out)
		if done {
			return err
		}
		lastErr = err

		var apiErr *APIError
		retryAfter := ""
		if errors.As(err, &apiErr) {
			retryAfter = resp.Header.Get("Retry-After")
		}
		if attempt == c.maxRetries {
			return lastErr
		}
		if err := c.sleepBackoff(ctx, attempt, retryAfter); err != nil {
			return err
		}
	}
	return lastErr
}

// handleResponse consumes resp. It returns done == true when the call is
// finished (successfully or with a non-retryable error), and done == false
// with the error to retry on.
func (c *Client) handleResponse(ctx context.Context, resp *http.Response, method string, out any) (done bool, err error) {
	defer func() { _ = resp.Body.Close() }()

	logDebug(ctx, "hubspot API response", map[string]any{
		"status": resp.StatusCode,
	})

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil || resp.StatusCode == http.StatusNoContent {
			// Drain so the connection can be reused.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
			return true, nil
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return true, fmt.Errorf("hubspot client: reading response body: %w", err)
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return true, nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return true, fmt.Errorf("hubspot client: decoding response body: %w", err)
		}
		return true, nil
	}

	apiErr := decodeAPIError(resp)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		// Never retry the DAILY policy: waiting it out could take hours.
		if apiErr.PolicyName == "DAILY" {
			return true, apiErr
		}
		return false, apiErr
	case resp.StatusCode >= 500 && isIdempotent(method):
		return false, apiErr
	default:
		return true, apiErr
	}
}

// decodeAPIError reads a non-2xx response body (up to 1 MiB) and decodes
// HubSpot's structured error shape, falling back to a raw body snippet.
func decodeAPIError(resp *http.Response) *APIError {
	apiErr := &APIError{StatusCode: resp.StatusCode}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil {
		apiErr.Message = fmt.Sprintf("failed to read error response body: %v", err)
		return apiErr
	}
	var body struct {
		Status        string `json:"status"`
		Message       string `json:"message"`
		Category      string `json:"category"`
		CorrelationID string `json:"correlationId"`
		PolicyName    string `json:"policyName"`
	}
	if json.Unmarshal(data, &body) == nil && (body.Message != "" || body.Category != "" || body.Status != "") {
		apiErr.Message = body.Message
		apiErr.Category = body.Category
		apiErr.CorrelationID = body.CorrelationID
		apiErr.PolicyName = body.PolicyName
		return apiErr
	}
	snippet := strings.TrimSpace(string(data))
	if len(snippet) > 512 {
		snippet = snippet[:512] + "..."
	}
	apiErr.Message = snippet
	return apiErr
}

// sleepBackoff waits before the next retry attempt. It honors a Retry-After
// header value in seconds (including "0"); otherwise it uses exponential
// backoff with jitter, starting at retryWaitMin and capped at retryWaitMax.
func (c *Client) sleepBackoff(ctx context.Context, attempt int, retryAfter string) error {
	var wait time.Duration
	if retryAfter != "" {
		if secs, err := strconv.Atoi(retryAfter); err == nil && secs >= 0 {
			wait = time.Duration(secs) * time.Second
		}
	}
	if retryAfter == "" {
		wait = c.retryWaitMin << uint(attempt)
		if wait > c.retryWaitMax || wait <= 0 {
			wait = c.retryWaitMax
		}
		// Full jitter: pick uniformly in [wait/2, wait).
		wait = wait/2 + time.Duration(rand.Int63n(int64(wait/2)+1))
	}
	if wait > c.retryWaitMax {
		wait = c.retryWaitMax
	}

	logDebug(ctx, "hubspot API retrying after backoff", map[string]any{
		"wait":    wait.String(),
		"attempt": attempt + 1,
	})

	if wait <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Page is HubSpot's uniform cursor-paginated response envelope:
// {"results":[...],"paging":{"next":{"after":"..."}}}.
type Page[T any] struct {
	Results []T     `json:"results"`
	Paging  *Paging `json:"paging"`
}

// Paging holds the cursor to the next page, when one exists.
type Paging struct {
	Next *NextPage `json:"next"`
}

// NextPage carries the "after" cursor for the next page of results.
type NextPage struct {
	After string `json:"after"`
	Link  string `json:"link"`
}

// CollectPages GETs path repeatedly, following HubSpot's cursor envelope by
// passing paging.next.after back as the "after" query parameter, and returns
// all accumulated results.
func CollectPages[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	q := url.Values{}
	for k, v := range query {
		q[k] = append([]string(nil), v...)
	}

	var all []T
	for {
		var page Page[T]
		if err := c.Get(ctx, path, q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		if page.Paging == nil || page.Paging.Next == nil || page.Paging.Next.After == "" {
			return all, nil
		}
		q.Set("after", page.Paging.Next.After)
	}
}
