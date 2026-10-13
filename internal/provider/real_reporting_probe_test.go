// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRealReportingAvailable pins the probe that lets the real-portal
// reporting tests skip (and the sweepers no-op) when the test portal has not
// opted into the Reporting API beta: 200 => available, 403 => unavailable
// with an actionable reason, anything else => error. Hermetic: it points the
// real-layer helpers at an httptest server. Not parallel (swaps realAPIBase).
func TestRealReportingAvailable(t *testing.T) {
	cases := []struct {
		status     int
		wantOK     bool
		wantReason string
		wantErr    string
	}{
		{status: http.StatusOK, wantOK: true},
		{status: http.StatusForbidden, wantReason: "has not opted into the Analytics Reporting API beta"},
		{status: http.StatusInternalServerError, wantErr: "returned status 500"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != realReportingBase+"/dashboards" || r.Header.Get("Authorization") != "Bearer tok" {
					t.Errorf("unexpected probe request %s %s", r.Method, r.URL)
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			orig := realAPIBase
			realAPIBase = srv.URL
			defer func() { realAPIBase = orig }()

			ok, reason, err := realReportingAvailable("tok")
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if !strings.Contains(reason, tc.wantReason) {
				t.Fatalf("reason = %q, want containing %q", reason, tc.wantReason)
			}
		})
	}
}
