// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider_test

// OpenAPI contract test for the Reporting API fake (fake_reporting_test.go):
// every endpoint the provider uses is driven through the fake and both the
// request and the response are validated against HubSpot's published OpenAPI
// spec with kin-openapi (dev-docs/research/08-tdd.md, "validate the fake
// against HubSpot's published OpenAPI specs").
//
// HubSpot's spec is proprietary ("not licensed for external use"), so it is
// NEVER vendored into this repo: the test downloads it at run time from a
// pinned commit. It is opt-in so `make test` stays hermetic/offline:
//
//	HUBSPOT_OPENAPI_CONTRACT=1 go test ./internal/provider/ -run TestReportingContract -count=1 -v
//	make test-contract
//
// HUBSPOT_OPENAPI_SPEC_FILE=/path/to/reporting.json uses a local copy
// instead of downloading. Once opted in, a download failure fails the test.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

const (
	// reportingSpecURL pins the spec to a commit of
	// HubSpot/HubSpot-public-api-spec-collection; bump deliberately.
	reportingSpecURL = "https://raw.githubusercontent.com/HubSpot/HubSpot-public-api-spec-collection/" +
		"3c63079c58e039bac5a53698fa64c5d0123a37d3/PublicApiSpecs/Analytics/Reporting/Rollouts/327896/2027-03-beta/reporting.json"
	reportingAPIPrefix = "/analytics/reporting/2027-03-beta"
)

// loadReportingSpec loads (download or local file) and validates the spec.
func loadReportingSpec(t *testing.T) *openapi3.T {
	t.Helper()
	var data []byte
	if p := os.Getenv("HUBSPOT_OPENAPI_SPEC_FILE"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("reading HUBSPOT_OPENAPI_SPEC_FILE: %v", err)
		}
		data = b
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reportingSpecURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("downloading the Reporting OpenAPI spec (set HUBSPOT_OPENAPI_SPEC_FILE to use a local copy): %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("downloading the Reporting OpenAPI spec: HTTP %d from %s", resp.StatusCode, reportingSpecURL)
		}
		if data, err = io.ReadAll(resp.Body); err != nil {
			t.Fatalf("downloading the Reporting OpenAPI spec: %v", err)
		}
	}

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(data)
	if err != nil {
		t.Fatalf("parsing the Reporting OpenAPI spec: %v", err)
	}

	// Spec quirk: the PATCH request schemas type `description` and
	// `businessUnitId` as `{"type": "object", "properties": {}}` (a generated
	// Optional<String> wrapper), while their descriptions — and the real API —
	// take a string, or null to clear/reset. Model the documented semantics.
	for _, name := range []string{"PublicDashboardUpdateRequest", "PublicReportUpdateRequest"} {
		ref := doc.Components.Schemas[name]
		if ref == nil || ref.Value == nil {
			t.Fatalf("spec has no %s schema", name)
		}
		for _, field := range []string{"description", "businessUnitId"} {
			old := ref.Value.Properties[field]
			if old == nil || old.Value == nil {
				t.Fatalf("spec %s has no %s property", name, field)
			}
			s := openapi3.NewStringSchema()
			s.Nullable = true
			s.Description = old.Value.Description
			ref.Value.Properties[field] = openapi3.NewSchemaRef("", s)
		}
	}

	if err := doc.Validate(loader.Context,
		openapi3.DisableExamplesValidation(),
		// The spec puts `description` next to `$ref` (default responses).
		openapi3.AllowExtraSiblingFields("description"),
	); err != nil {
		t.Fatalf("the Reporting OpenAPI spec does not validate: %v", err)
	}
	return doc
}

// contractClient sends requests to the fake and validates each request and
// response against the spec.
type contractClient struct {
	t      *testing.T
	base   string
	router routers.Router
	opts   *openapi3filter.Options
	// covered records "METHOD /spec/path/template" of every validated call.
	covered map[string]bool
}

// call performs METHOD path?query with an optional JSON body, validates the
// exchange, asserts the status and returns the decoded response body (nil
// when empty).
func (c *contractClient) call(method, path string, query url.Values, body any, wantStatus int) map[string]any {
	c.t.Helper()
	var raw []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		raw = b
	}
	u := c.base + reportingAPIPrefix + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	ctx := context.Background()
	newReq := func() *http.Request {
		var r io.Reader
		if raw != nil {
			r = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, r)
		if err != nil {
			c.t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer pat-na1-contract-test")
		if raw != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req
	}
	label := method + " " + path

	// Request validation (on a separate request so the sent body is intact).
	vreq := newReq()
	route, pathParams, err := c.router.FindRoute(vreq)
	if err != nil {
		c.t.Fatalf("%s: no matching operation in the spec: %v", label, err)
	}
	reqInput := &openapi3filter.RequestValidationInput{
		Request: vreq, PathParams: pathParams, Route: route, Options: c.opts,
	}
	if err := openapi3filter.ValidateRequest(ctx, reqInput); err != nil {
		c.t.Errorf("%s: request violates the spec: %v", label, err)
	}
	c.covered[method+" "+strings.TrimPrefix(route.Path, reportingAPIPrefix)] = true

	resp, err := http.DefaultClient.Do(newReq())
	if err != nil {
		c.t.Fatalf("%s: %v", label, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		c.t.Fatalf("%s: status %d, want %d; body: %s", label, resp.StatusCode, wantStatus, respBody)
	}
	// Success codes must be declared explicitly (the spec's `default`
	// response would otherwise accept e.g. 200 where HubSpot returns 204).
	if resp.StatusCode < 400 && route.Operation.Responses.Status(resp.StatusCode) == nil {
		c.t.Errorf("%s: status %d is not declared by the spec for this operation", label, resp.StatusCode)
	}
	respInput := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: reqInput,
		Status:                 resp.StatusCode,
		Header:                 resp.Header,
		Options:                c.opts,
	}
	respInput.SetBodyBytes(respBody)
	if err := openapi3filter.ValidateResponse(ctx, respInput); err != nil {
		c.t.Errorf("%s: response violates the spec: %v\nbody: %s", label, err, respBody)
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(respBody, &out); err != nil {
		c.t.Fatalf("%s: response is not a JSON object: %v", label, err)
	}
	return out
}

func idOf(t *testing.T, obj map[string]any) string {
	t.Helper()
	id, ok := obj["id"].(string)
	if !ok || id == "" {
		t.Fatalf("response has no string id: %v", obj)
	}
	return id
}

func TestReportingContract(t *testing.T) {
	if os.Getenv("HUBSPOT_OPENAPI_CONTRACT") != "1" {
		t.Skip("set HUBSPOT_OPENAPI_CONTRACT=1 to validate the Reporting API fake against HubSpot's OpenAPI spec (downloads the spec)")
	}
	doc := loadReportingSpec(t)

	f, srv := newFakeHubSpot(t)
	doc.Servers = openapi3.Servers{{URL: srv.URL}}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatalf("building router: %v", err)
	}
	c := &contractClient{
		t: t, base: srv.URL, router: router, covered: map[string]bool{},
		opts: &openapi3filter.Options{
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
			MultiError:         true,
		},
	}

	// The provider always asks for the optional sections this way.
	dashProps := url.Values{"properties": {"permissions,tags,widgets"}}
	reportProps := url.Values{"properties": {"permissions,tags"}}
	with := func(base url.Values, kv ...string) url.Values {
		q := url.Values{}
		for k, v := range base {
			q[k] = append([]string(nil), v...)
		}
		for i := 0; i+1 < len(kv); i += 2 {
			q.Add(kv[i], kv[i+1])
		}
		return q
	}
	everyoneView := map[string]any{"permissionType": "EVERYONE_VIEW"}

	// UI-built reports (the API cannot create reports from scratch).
	r1, r2, r3 := f.seedReport("Contract pipeline"), f.seedReport("Contract revenue"), f.seedReport("Contract churn")
	f.tagReportingObject("reports", r1, "77", "Sales")

	// ---- Dashboards ------------------------------------------------------

	d := idOf(t, c.call(http.MethodPost, "/dashboards", nil, map[string]any{
		"name":           "Contract dashboard",
		"description":    "created by the contract test",
		"permissions":    everyoneView,
		"reportIdsToAdd": []string{r1, r2},
	}, http.StatusOK))
	f.tagReportingObject("dashboards", d, "88", "Exec")

	got := c.call(http.MethodGet, "/dashboards/"+d, dashProps, nil, http.StatusOK)
	if w, _ := got["widgets"].([]any); len(w) != 2 {
		t.Errorf("dashboard widgets = %v, want 2 (reportIdsToAdd)", got["widgets"])
	}

	c.call(http.MethodGet, "/dashboards", with(dashProps,
		"limit", "100", "q", "contract", "ids", d, "ownerUserIds", fakeReportingUserID,
		"tagIds", "88", "businessUnitIds", "0"), nil, http.StatusOK)
	c.call(http.MethodGet, "/dashboards", url.Values{"limit": {"100"}, "archived": {"true"}}, nil, http.StatusOK)

	// PATCH: metadata, SPECIFIC permissions (dashboards: ARRAY form),
	// description null, then archive / restore.
	c.call(http.MethodPatch, "/dashboards/"+d, nil, map[string]any{
		"name": "Contract dashboard (renamed)", "description": "new description",
		"businessUnitId": "0", "ownerUserId": fakeReportingUserID,
	}, http.StatusOK)
	c.call(http.MethodPatch, "/dashboards/"+d, nil, map[string]any{
		"permissions": map[string]any{
			"permissionType": "SPECIFIC",
			"specificPermissions": []any{
				map[string]any{"permissionType": "VIEW", "grants": []any{map[string]any{"grantType": "TEAM", "granteeId": "42"}}},
				map[string]any{"permissionType": "EDIT", "grants": []any{map[string]any{"grantType": "USER", "granteeId": "7"}}},
			},
		},
	}, http.StatusOK)
	c.call(http.MethodPatch, "/dashboards/"+d, nil, map[string]any{"description": nil, "businessUnitId": nil}, http.StatusOK)
	got = c.call(http.MethodGet, "/dashboards/"+d, dashProps, nil, http.StatusOK)
	if _, has := got["description"]; has {
		t.Errorf("description should be cleared by PATCH null: %v", got["description"])
	}

	// Widgets.
	c.call(http.MethodPut, "/dashboards/"+d+"/widgets/"+r3, nil, nil, http.StatusOK)
	c.call(http.MethodDelete, "/dashboards/"+d+"/widgets/"+r2, nil, nil, http.StatusOK)
	c.call(http.MethodPost, "/dashboards/"+d+"/batch/widgets", nil, map[string]any{"inputs": []string{r2}}, http.StatusOK)

	// Clone with and without cloned reports.
	cloneRefs := idOf(t, c.call(http.MethodPost, "/dashboards/"+d+"/clone", nil, map[string]any{
		"name": "Contract clone (shared reports)", "permissions": everyoneView, "cloneReports": false,
	}, http.StatusOK))
	cloneCopies := idOf(t, c.call(http.MethodPost, "/dashboards/"+d+"/clone", nil, map[string]any{
		"name": "Contract clone (copied reports)", "permissions": map[string]any{"permissionType": "PRIVATE"}, "cloneReports": true,
	}, http.StatusOK))

	// Archive / confirm / restore (single), then batch.
	c.call(http.MethodPatch, "/dashboards/"+cloneRefs, nil, map[string]any{"archived": true}, http.StatusOK)
	c.call(http.MethodGet, "/dashboards/"+cloneRefs, with(dashProps, "archived", "true"), nil, http.StatusOK)
	c.call(http.MethodPatch, "/dashboards/"+cloneRefs, nil, map[string]any{"archived": false}, http.StatusOK)
	c.call(http.MethodPost, "/dashboards/batch/archive", nil, map[string]any{"inputs": []string{cloneRefs, cloneCopies}}, http.StatusNoContent)
	c.call(http.MethodPost, "/dashboards/batch/restore", nil, map[string]any{"inputs": []string{cloneRefs}}, http.StatusOK)

	// ---- Reports ---------------------------------------------------------

	c.call(http.MethodGet, "/reports", with(reportProps, "limit", "100", "dashboardId", d), nil, http.StatusOK)
	c.call(http.MethodGet, "/reports", with(reportProps,
		"limit", "100", "q", "contract", "ownerUserIds", fakeReportingUserID, "tagIds", "77",
		"businessUnitIds", "0", "ids", r1), nil, http.StatusOK)
	c.call(http.MethodGet, "/reports", url.Values{"limit": {"100"}, "onDashboard": {"false"}}, nil, http.StatusOK)

	// Pagination: walk every page with limit=1 and the `after` cursor.
	pages, seen := 0, 0
	for after := ""; ; pages++ {
		q := with(reportProps, "limit", "1")
		if after != "" {
			q.Set("after", after)
		}
		page := c.call(http.MethodGet, "/reports", q, nil, http.StatusOK)
		results, _ := page["results"].([]any)
		seen += len(results)
		paging, _ := page["paging"].(map[string]any)
		next, _ := paging["next"].(map[string]any)
		if next == nil {
			break
		}
		after, _ = next["after"].(string)
		if pages > 50 {
			t.Fatal("pagination does not terminate")
		}
	}
	if pages < 2 || seen < 3 {
		t.Errorf("pagination walked %d pages / %d reports, want several", pages+1, seen)
	}

	c.call(http.MethodGet, "/reports/"+r1, reportProps, nil, http.StatusOK)

	// PATCH: metadata, SPECIFIC permissions (reports: SINGLE object form).
	c.call(http.MethodPatch, "/reports/"+r1, nil, map[string]any{
		"name": "Contract pipeline (renamed)", "description": "report description",
		"businessUnitId": "0", "ownerUserId": fakeReportingUserID,
	}, http.StatusOK)
	c.call(http.MethodPatch, "/reports/"+r1, nil, map[string]any{
		"permissions": map[string]any{
			"permissionType": "SPECIFIC",
			"specificPermissions": map[string]any{
				"permissionType": "VIEW", "grants": []any{map[string]any{"grantType": "TEAM", "granteeId": "42"}},
			},
		},
	}, http.StatusOK)
	c.call(http.MethodPatch, "/reports/"+r1, nil, map[string]any{"description": nil}, http.StatusOK)
	c.call(http.MethodGet, "/reports/"+r1, reportProps, nil, http.StatusOK)

	clone := idOf(t, c.call(http.MethodPost, "/reports/"+r1+"/clone", nil, map[string]any{
		"name": "Contract pipeline (clone)", "permissions": map[string]any{"permissionType": "EVERYONE_EDIT"},
	}, http.StatusOK))

	c.call(http.MethodPatch, "/reports/"+clone, nil, map[string]any{"archived": true}, http.StatusOK)
	c.call(http.MethodGet, "/reports/"+clone, with(reportProps, "archived", "true"), nil, http.StatusOK)
	c.call(http.MethodPatch, "/reports/"+clone, nil, map[string]any{"archived": false}, http.StatusOK)
	c.call(http.MethodPost, "/reports/batch/archive", nil, map[string]any{"inputs": []string{clone}}, http.StatusNoContent)
	c.call(http.MethodPost, "/reports/batch/restore", nil, map[string]any{"inputs": []string{clone}}, http.StatusOK)

	// ---- Errors: the HubSpot error envelope ------------------------------

	c.call(http.MethodGet, "/dashboards/999999", nil, nil, http.StatusNotFound)
	c.call(http.MethodGet, "/reports/"+r1, url.Values{"archived": {"true"}}, nil, http.StatusNotFound)
	c.call(http.MethodPatch, "/reports/"+r1, nil, map[string]any{"archived": true, "name": "x"}, http.StatusBadRequest)
	grant := []any{map[string]any{"grantType": "USER", "granteeId": "7"}}
	c.call(http.MethodPatch, "/dashboards/"+d, nil, map[string]any{ // same grantee in VIEW and EDIT
		"permissions": map[string]any{"permissionType": "SPECIFIC", "specificPermissions": []any{
			map[string]any{"permissionType": "VIEW", "grants": grant},
			map[string]any{"permissionType": "EDIT", "grants": grant},
		}},
	}, http.StatusBadRequest)

	// Every operation the provider uses must have been exercised.
	for _, op := range []string{
		"GET /dashboards", "POST /dashboards", "GET /dashboards/{dashboardId}", "PATCH /dashboards/{dashboardId}",
		"POST /dashboards/{dashboardId}/clone", "PUT /dashboards/{dashboardId}/widgets/{reportId}",
		"DELETE /dashboards/{dashboardId}/widgets/{reportId}", "POST /dashboards/{dashboardId}/batch/widgets",
		"POST /dashboards/batch/archive", "POST /dashboards/batch/restore",
		"GET /reports", "GET /reports/{reportId}", "PATCH /reports/{reportId}", "POST /reports/{reportId}/clone",
		"POST /reports/batch/archive", "POST /reports/batch/restore",
	} {
		if !c.covered[op] {
			t.Errorf("operation %s was not exercised", op)
		}
	}
	t.Logf("validated %d distinct operations against %s", len(c.covered), reportingSpecURL)
}
