// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

// Package hscli gets HubSpot access tokens from a HubSpot CLI login: the
// personal access key that `hs account auth` stores in ~/.hscli/config.yml.
// It exchanges the key for a short-lived access token the same way the CLI
// does, and never writes to the CLI's config.
package hscli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	exchangePath = "/localdevauth/v1/auth/refresh"
	// renewBefore matches the CLI, which treats a token as stale five minutes
	// before it expires.
	renewBefore = 5 * time.Minute
	// maxErrorBody caps how much of a failed exchange response is read.
	maxErrorBody = 64 << 10
)

// DefaultConfigPath returns the HubSpot CLI's global config file,
// ~/.hscli/config.yml.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the HubSpot CLI config: %w", err)
	}
	return filepath.Join(home, ".hscli", "config.yml"), nil
}

// Account is a HubSpot CLI account that authenticates with a personal access
// key.
type Account struct {
	Name string
	ID   int64

	personalAccessKey string
}

type config struct {
	DefaultAccount string `yaml:"defaultAccount"`
	Accounts       []struct {
		Name              string `yaml:"name"`
		AccountID         int64  `yaml:"accountId"`
		AuthType          string `yaml:"authType"`
		PersonalAccessKey string `yaml:"personalAccessKey"`
	} `yaml:"accounts"`
}

// LoadAccount reads the CLI config at path and returns the account whose name
// or account ID is selector, or the CLI's default account when selector is
// empty.
func LoadAccount(path, selector string) (Account, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the CLI's own config file
	if errors.Is(err, os.ErrNotExist) {
		return Account{}, fmt.Errorf("no HubSpot CLI config at %s; log in with `hs account auth`", path)
	}
	if err != nil {
		return Account{}, fmt.Errorf("reading the HubSpot CLI config: %w", err)
	}
	var cfg config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		// yaml errors quote only line numbers and types, never values.
		return Account{}, fmt.Errorf("parsing the HubSpot CLI config %s: %w", path, err)
	}

	if selector == "" {
		selector = cfg.DefaultAccount
		if selector == "" {
			return Account{}, fmt.Errorf("the HubSpot CLI config %s has no default account; set hs_cli_account or run `hs account use`", path)
		}
	}
	for _, a := range cfg.Accounts {
		if a.Name != selector && strconv.FormatInt(a.AccountID, 10) != selector {
			continue
		}
		if !strings.EqualFold(a.AuthType, "personalaccesskey") || a.PersonalAccessKey == "" {
			return Account{}, fmt.Errorf("HubSpot CLI account %q does not use a personal access key; log in to it with `hs account auth`", selector)
		}
		return Account{Name: a.Name, ID: a.AccountID, personalAccessKey: a.PersonalAccessKey}, nil
	}
	return Account{}, fmt.Errorf("the HubSpot CLI config %s has no account %q; run `hs account list` to see its accounts", path, selector)
}

// TokenSource exchanges an account's personal access key for access tokens,
// and exchanges it again shortly before each token expires. It is safe for
// concurrent use.
type TokenSource struct {
	baseURL    string
	account    Account
	httpClient *http.Client

	mu         sync.Mutex
	token      string
	expiration time.Time
}

// NewTokenSource returns a TokenSource that exchanges account's key at
// baseURL. A nil httpClient means http.DefaultClient.
func NewTokenSource(baseURL string, account Account, httpClient *http.Client) *TokenSource {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &TokenSource{
		baseURL:    strings.TrimRight(baseURL, "/"),
		account:    account,
		httpClient: httpClient,
	}
}

// Token returns an access token valid for at least a few more minutes.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Now().Before(s.expiration.Add(-renewBefore)) {
		return s.token, nil
	}
	token, expiration, err := s.exchange(ctx)
	if err != nil {
		return "", err
	}
	s.token, s.expiration = token, expiration
	return token, nil
}

func (s *TokenSource) exchange(ctx context.Context) (string, time.Time, error) {
	body, err := json.Marshal(map[string]string{"encodedOAuthRefreshToken": s.account.personalAccessKey})
	if err != nil {
		return "", time.Time{}, err
	}
	u := s.baseURL + exchangePath + "?portalId=" + strconv.FormatInt(s.account.ID, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("exchanging the HubSpot CLI personal access key for account %q: %w", s.account.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// The message is not quoted: HubSpot may echo part of the key in it.
		var e struct {
			Category string `json:"category"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, maxErrorBody)).Decode(&e)
		detail := ""
		if e.Category != "" {
			detail = " (" + e.Category + ")"
		}
		return "", time.Time{}, fmt.Errorf("HubSpot refused the HubSpot CLI personal access key for account %q: HTTP %d%s; log in again with `hs account auth`",
			s.account.Name, resp.StatusCode, detail)
	}

	var out struct {
		Token           string `json:"oauthAccessToken"`
		ExpiresAtMillis int64  `json:"expiresAtMillis"`
		HubID           int64  `json:"hubId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", time.Time{}, errors.New("the HubSpot CLI key exchange returned a response that is not JSON")
	}
	if out.Token == "" || out.ExpiresAtMillis == 0 {
		return "", time.Time{}, errors.New("the HubSpot CLI key exchange returned no access token")
	}
	if out.HubID != s.account.ID {
		return "", time.Time{}, fmt.Errorf("the HubSpot CLI personal access key for account %q (%d) is for account %d",
			s.account.Name, s.account.ID, out.HubID)
	}
	return out.Token, time.UnixMilli(out.ExpiresAtMillis), nil
}
