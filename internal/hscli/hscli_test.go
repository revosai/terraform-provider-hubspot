// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package hscli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testConfig = `defaultAccount: 222
accounts:
  - name: production
    accountId: 111
    env: prod
    authType: personalaccesskey
    personalAccessKey: key-production
  - name: sandbox
    accountId: 222
    env: prod
    authType: personalaccesskey
    personalAccessKey: key-sandbox
  - name: legacy
    accountId: 333
    authType: apikey
`

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAccount(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, testConfig)

	tests := []struct {
		selector string
		wantName string
		wantID   int64
	}{
		{"", "sandbox", 222},
		{"production", "production", 111},
		{"111", "production", 111},
		{"sandbox", "sandbox", 222},
	}
	for _, tt := range tests {
		a, err := LoadAccount(p, tt.selector)
		if err != nil {
			t.Errorf("LoadAccount(%q): %v", tt.selector, err)
			continue
		}
		if a.Name != tt.wantName || a.ID != tt.wantID {
			t.Errorf("LoadAccount(%q) = %s (%d), want %s (%d)", tt.selector, a.Name, a.ID, tt.wantName, tt.wantID)
		}
	}
}

func TestLoadAccount_errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   string // "" means the file does not exist
		selector string
		wantErr  string
	}{
		{"no config", "", "", "hs account auth"},
		{"unknown account", testConfig, "staging", `no account "staging"`},
		{"not a personal access key", testConfig, "legacy", "personal access key"},
		{"no default", "accounts:\n  - name: a\n    accountId: 1\n    authType: personalaccesskey\n    personalAccessKey: k\n", "", "no default account"},
		{"empty key", "accounts:\n  - name: a\n    accountId: 1\n    authType: personalaccesskey\n", "a", "personal access key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), "missing.yml")
			if tt.config != "" {
				p = writeConfig(t, tt.config)
			}
			_, err := LoadAccount(p, tt.selector)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "key-") {
				t.Fatalf("error quotes a personal access key: %v", err)
			}
		})
	}
}

// fakeExchange serves the HubSpot CLI's key exchange. Each call issues a new
// token that expires after lifetime.
type fakeExchange struct {
	mu       sync.Mutex
	calls    int
	key      string
	portal   string
	hubID    int64
	lifetime time.Duration
	status   int
}

func (f *fakeExchange) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if r.Method != http.MethodPost || r.URL.Path != "/localdevauth/v1/auth/refresh" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var body struct {
		Key string `json:"encodedOAuthRefreshToken"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.key, f.portal = body.Key, r.URL.Query().Get("portalId")
	if f.status != 0 {
		w.WriteHeader(f.status)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "error", "category": "INVALID_AUTHENTICATION", "message": "key " + body.Key + " was revoked",
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"oauthAccessToken": "token-" + string(rune('0'+f.calls)),
		"expiresAtMillis":  time.Now().Add(f.lifetime).UnixMilli(),
		"hubId":            f.hubID,
	})
}

func newExchange(t *testing.T, f *fakeExchange) *TokenSource {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return NewTokenSource(srv.URL, Account{Name: "sandbox", ID: 222, personalAccessKey: "key-sandbox"}, nil)
}

func TestTokenSource_exchangesAndCaches(t *testing.T) {
	t.Parallel()
	f := &fakeExchange{hubID: 222, lifetime: 30 * time.Minute}
	ts := newExchange(t, f)

	for range 3 {
		tok, err := ts.Token(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if tok != "token-1" {
			t.Fatalf("token = %q, want token-1", tok)
		}
	}
	if f.calls != 1 {
		t.Errorf("exchanged %d times, want 1", f.calls)
	}
	if f.key != "key-sandbox" || f.portal != "222" {
		t.Errorf("exchange sent key %q for portal %q", f.key, f.portal)
	}
}

func TestTokenSource_renewsBeforeExpiry(t *testing.T) {
	t.Parallel()
	// Inside the renewal window from the start, so every call exchanges again.
	f := &fakeExchange{hubID: 222, lifetime: 2 * time.Minute}
	ts := newExchange(t, f)

	first, err := ts.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := ts.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first == second || f.calls != 2 {
		t.Errorf("tokens %q then %q after %d exchanges; want a renewal", first, second, f.calls)
	}
}

func TestTokenSource_errors(t *testing.T) {
	t.Parallel()

	t.Run("rejected key", func(t *testing.T) {
		t.Parallel()
		ts := newExchange(t, &fakeExchange{hubID: 222, status: http.StatusUnauthorized})
		_, err := ts.Token(context.Background())
		if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "hs account auth") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), "key-sandbox") {
			t.Fatalf("error quotes the personal access key: %v", err)
		}
	})

	t.Run("other account", func(t *testing.T) {
		t.Parallel()
		ts := newExchange(t, &fakeExchange{hubID: 999, lifetime: 30 * time.Minute})
		_, err := ts.Token(context.Background())
		if err == nil || !strings.Contains(err.Error(), "999") {
			t.Fatalf("err = %v", err)
		}
	})
}
