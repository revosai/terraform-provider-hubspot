package client_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/revosai/terraform-provider-hubspot/internal/client"
)

func newTestClient(t *testing.T, handler http.Handler) *client.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := client.New(client.Config{
		BaseURL:     srv.URL,
		AccessToken: "pat-na1-test-token",
		UserAgent:   "terraform-provider-hubspot/test",
		MaxRetries:  3,
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return c
}

func TestClient_SendsAuthAndUserAgent(t *testing.T) {
	t.Parallel()
	var gotAuth, gotUA string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))

	var out map[string]any
	if err := c.Get(context.Background(), "/ping", nil, &out); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotAuth != "Bearer pat-na1-test-token" {
		t.Errorf("Authorization = %q, want Bearer token", gotAuth)
	}
	if gotUA != "terraform-provider-hubspot/test" {
		t.Errorf("User-Agent = %q", gotUA)
	}
}

func TestClient_NotFoundIsTyped(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"status":"error","message":"Object not found","category":"OBJECT_NOT_FOUND","correlationId":"abc-123"}`)
	}))

	err := c.Get(context.Background(), "/crm/v3/properties/contacts/nope", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !client.IsNotFound(err) {
		t.Errorf("IsNotFound = false, want true; err = %v", err)
	}
}

func TestClient_ConflictIsTyped(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"status":"error","message":"Flow revision id 1 is not the latest revision id 2","category":"CONFLICT","correlationId":"abc-123"}`)
	}))

	err := c.Put(context.Background(), "/automation/v4/flows/1", map[string]any{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !client.IsConflict(err) {
		t.Errorf("IsConflict = false, want true; err = %v", err)
	}
	if client.IsNotFound(err) {
		t.Error("IsNotFound = true for a 409, want false")
	}
}

func TestClient_DecodesHubSpotErrorBody(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"status":"error","message":"Invalid input JSON","category":"VALIDATION_ERROR","correlationId":"corr-42"}`)
	}))

	err := c.Post(context.Background(), "/crm/v3/properties/contacts", map[string]any{"bad": true}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *client.APIError
	if !client.AsAPIError(err, &apiErr) {
		t.Fatalf("error is not *APIError: %v", err)
	}
	if apiErr.StatusCode != 400 || apiErr.Category != "VALIDATION_ERROR" || apiErr.Message != "Invalid input JSON" || apiErr.CorrelationID != "corr-42" {
		t.Errorf("unexpected APIError: %+v", apiErr)
	}
}

func TestClient_RetriesSecondlyRateLimit(t *testing.T) {
	t.Parallel()
	var calls int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"status":"error","message":"Rate limit exceeded","category":"RATE_LIMITS","policyName":"TEN_SECONDLY_ROLLING"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))

	var out map[string]any
	if err := c.Get(context.Background(), "/retry-me", nil, &out); err != nil {
		t.Fatalf("expected retry to succeed, got: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("calls = %d, want 2", n)
	}
}

func TestClient_DailyLimitDoesNotRetry(t *testing.T) {
	t.Parallel()
	var calls int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"status":"error","message":"Daily limit reached","category":"RATE_LIMITS","policyName":"DAILY"}`)
	}))

	err := c.Get(context.Background(), "/daily", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("calls = %d, want 1 (DAILY must not retry)", n)
	}
	var apiErr *client.APIError
	if !client.AsAPIError(err, &apiErr) || apiErr.PolicyName != "DAILY" {
		t.Errorf("expected DAILY APIError, got: %v", err)
	}
}

func TestClient_RetriesServerErrorOnIdempotentVerbs(t *testing.T) {
	t.Parallel()
	var calls int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))

	if err := c.Get(context.Background(), "/flaky", nil, nil); err != nil {
		t.Fatalf("expected retry to succeed, got: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("calls = %d, want 2", n)
	}
}

func TestClient_DoesNotRetryServerErrorOnPost(t *testing.T) {
	t.Parallel()
	var calls int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))

	err := c.Post(context.Background(), "/create", map[string]any{"a": 1}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("calls = %d, want 1 (POST must not retry on 5xx)", n)
	}
}

func TestClient_PaginationFollowsCursor(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("after") {
		case "":
			_, _ = fmt.Fprint(w, `{"results":[{"name":"a"},{"name":"b"}],"paging":{"next":{"after":"cursor-1"}}}`)
		case "cursor-1":
			_, _ = fmt.Fprint(w, `{"results":[{"name":"c"}]}`)
		default:
			t.Errorf("unexpected cursor %q", r.URL.Query().Get("after"))
			w.WriteHeader(http.StatusBadRequest)
		}
	}))

	type item struct {
		Name string `json:"name"`
	}
	items, err := client.CollectPages[item](context.Background(), c, "/crm/v3/things", nil)
	if err != nil {
		t.Fatalf("CollectPages: %v", err)
	}
	if len(items) != 3 || items[0].Name != "a" || items[2].Name != "c" {
		t.Errorf("unexpected items: %+v", items)
	}
}
