// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider_test

// Stateful fake of the Analytics Reporting API (public beta,
// /analytics/reporting/2027-03-beta), modeled on HubSpot's published OpenAPI
// spec (PublicDashboard / PublicReport). It emulates the behaviors the
// provider must handle:
//   - optional response sections: permissions, tags and widgets are only
//     returned when requested via ?properties=… (a provider that forgets to
//     ask sees them missing),
//   - archive-not-delete: PATCH {archived:true} (which cannot be combined with
//     other fields) or batch/archive; archived objects 404 unless
//     ?archived=true, and active ones 404 WITH ?archived=true,
//   - PATCH semantics: omitted fields unchanged, explicit null clears
//     description / resets businessUnitId to the default "0",
//   - best-effort reportIdsToAdd on dashboard create (unknown IDs skipped
//     silently), server-assigned widget layout,
//   - report clone (the only way to create a report), dashboard clone with
//     or without cloned reports,
//   - search with q / ids / owner / tag / business-unit / dashboard filters
//     and cursor pagination,
//   - a "beta not enabled" mode (403 on every reporting route).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// fakeReportingUserID is the user the fake attributes token writes to.
const fakeReportingUserID = "1"

type fakeGrant struct {
	GrantType string `json:"grantType"`
	GranteeID string `json:"granteeId"`
}

type fakeSpecificPermission struct {
	PermissionType string      `json:"permissionType"`
	Grants         []fakeGrant `json:"grants,omitempty"`
}

type fakeTag struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type fakeWidget struct {
	ReportID string `json:"reportId"`
	X        int64  `json:"x"`
	Y        int64  `json:"y"`
	Width    int64  `json:"width"`
	Height   int64  `json:"height"`
}

// fakeReportingObject is the common shape of a dashboard and a report.
type fakeReportingObject struct {
	ID              string
	Name            string
	Description     *string
	BusinessUnitID  string
	OwnerUserID     *string
	Archived        bool
	ArchivedAt      string
	CreatedAt       string
	CreatedByUserID string
	UpdatedAt       string
	UpdatedByUserID string
	LastViewedAt    string
	PermissionType  string
	// Dashboards may carry several specific permissions (VIEW and EDIT);
	// reports carry at most one.
	Specific []fakeSpecificPermission
	Tags     []fakeTag
	Widgets  []fakeWidget // dashboards only
}

type fakeReportingRequest struct {
	Method string
	Path   string // path below /analytics/reporting/2027-03-beta
	Query  string
	Body   []byte
}

type fakeReporting struct {
	dashboards map[string]*fakeReportingObject
	reports    map[string]*fakeReportingObject
	nextID     int64
	clock      int64
	disabled   bool // beta not enabled: 403 everywhere
	requests   []fakeReportingRequest
}

// rep returns the reporting state, creating it on first use. Callers must
// hold f.mu.
func (f *fakeHubSpot) rep() *fakeReporting {
	if f.reporting == nil {
		f.reporting = &fakeReporting{
			dashboards: map[string]*fakeReportingObject{},
			reports:    map[string]*fakeReportingObject{},
			nextID:     1000,
		}
	}
	return f.reporting
}

func (fr *fakeReporting) newID() string {
	fr.nextID++
	return strconv.FormatInt(fr.nextID, 10)
}

// tick advances the fake clock and returns an ISO 8601 timestamp.
func (fr *fakeReporting) tick() string {
	fr.clock++
	return fmt.Sprintf("2026-10-01T00:%02d:%02d.000Z", (fr.clock/60)%60, fr.clock%60)
}

// ---------------------------------------------------------------------------
// Test helpers (seeding / out-of-band mutation / introspection).
// ---------------------------------------------------------------------------

// seedReport creates a report as if built in the HubSpot UI (the API cannot
// create reports from scratch) and returns its ID. The report is
// EVERYONE_EDIT, owned by the token user.
func (f *fakeHubSpot) seedReport(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	fr := f.rep()
	now := fr.tick()
	owner := fakeReportingUserID
	o := &fakeReportingObject{
		ID: fr.newID(), Name: name, BusinessUnitID: "0", OwnerUserID: &owner,
		CreatedAt: now, CreatedByUserID: fakeReportingUserID, UpdatedAt: now, UpdatedByUserID: fakeReportingUserID,
		PermissionType: "EVERYONE_EDIT",
	}
	fr.reports[o.ID] = o
	return o.ID
}

// seedDashboard creates a dashboard as if built in the UI, with the given
// reports as widgets, and returns its ID.
func (f *fakeHubSpot) seedDashboard(name string, reportIDs ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	fr := f.rep()
	now := fr.tick()
	owner := fakeReportingUserID
	o := &fakeReportingObject{
		ID: fr.newID(), Name: name, BusinessUnitID: "0", OwnerUserID: &owner,
		CreatedAt: now, CreatedByUserID: fakeReportingUserID, UpdatedAt: now, UpdatedByUserID: fakeReportingUserID,
		PermissionType: "EVERYONE_EDIT",
	}
	for _, rid := range reportIDs {
		addWidget(o, rid)
	}
	fr.dashboards[o.ID] = o
	return o.ID
}

// tagReportingObject attaches a tag (tags are read-only via the API).
func (f *fakeHubSpot) tagReportingObject(kind, id, tagID, tagName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.rep().collection(kind)[id]
	o.Tags = append(o.Tags, fakeTag{ID: tagID, Name: tagName})
}

// mutateReportingObject applies fn to a dashboard ("dashboards") or report
// ("reports") out of band (UI edits, archive, layout moves…).
func (f *fakeHubSpot) mutateReportingObject(kind, id string, fn func(o *fakeReportingObject)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.rep().collection(kind)[id])
}

// deleteReportingObjectOOB hard-deletes an object out of band (disappears tests).
func (f *fakeHubSpot) deleteReportingObjectOOB(kind, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rep().collection(kind), id)
}

// reportingObject returns a copy of a stored object (nil if absent).
func (f *fakeHubSpot) reportingObject(kind, id string) *fakeReportingObject {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.rep().collection(kind)[id]
	if !ok {
		return nil
	}
	c := *o
	c.Widgets = append([]fakeWidget(nil), o.Widgets...)
	return &c
}

// reportingObjectsNamed returns the IDs of all objects (active and archived)
// with the given name.
func (f *fakeHubSpot) reportingObjectsNamed(kind, name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id, o := range f.rep().collection(kind) {
		if o.Name == name {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// setReportingDisabled simulates a portal that has not opted into the beta.
func (f *fakeHubSpot) setReportingDisabled(disabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rep().disabled = disabled
}

// reportingRequests returns every reporting request received so far
// (method, sub-path, raw query, body) for wire-level assertions.
func (f *fakeHubSpot) reportingRequests() []fakeReportingRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeReportingRequest(nil), f.rep().requests...)
}

// ---------------------------------------------------------------------------
// Routing.
// ---------------------------------------------------------------------------

func (fr *fakeReporting) collection(kind string) map[string]*fakeReportingObject {
	if kind == "dashboards" {
		return fr.dashboards
	}
	return fr.reports
}

// reportingRoute serves analytics/reporting/2027-03-beta/{rest...}. Caller
// holds f.mu.
func (f *fakeHubSpot) reportingRoute(w http.ResponseWriter, r *http.Request, rest []string) {
	fr := f.rep()
	body, _ := io.ReadAll(r.Body)
	fr.requests = append(fr.requests, fakeReportingRequest{
		Method: r.Method, Path: "/" + strings.Join(rest, "/"), Query: r.URL.RawQuery, Body: body,
	})
	if fr.disabled {
		writeHubSpotError(w, http.StatusForbidden, "MISSING_SCOPES",
			"This app hasn't been granted all required scopes to make this call.")
		return
	}
	if len(rest) == 0 || (rest[0] != "dashboards" && rest[0] != "reports") {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "unknown reporting path "+r.URL.Path)
		return
	}
	kind := rest[0]
	rest = rest[1:]
	m := r.Method

	switch {
	case len(rest) == 0 && m == http.MethodGet:
		fr.search(w, r, kind)
	case len(rest) == 0 && m == http.MethodPost && kind == "dashboards":
		fr.createDashboard(w, body)
	case len(rest) == 2 && rest[0] == "batch" && m == http.MethodPost && (rest[1] == "archive" || rest[1] == "restore"):
		fr.batchArchive(w, kind, body, rest[1] == "archive")
	case len(rest) == 1 && m == http.MethodGet:
		fr.get(w, r, kind, rest[0])
	case len(rest) == 1 && m == http.MethodPatch:
		fr.patch(w, kind, rest[0], body)
	case len(rest) == 2 && rest[1] == "clone" && m == http.MethodPost:
		fr.clone(w, kind, rest[0], body)
	case kind == "dashboards" && len(rest) == 3 && rest[1] == "widgets" && (m == http.MethodPut || m == http.MethodDelete):
		fr.widget(w, rest[0], rest[2], m == http.MethodPut)
	case kind == "dashboards" && len(rest) == 3 && rest[1] == "batch" && rest[2] == "widgets" && m == http.MethodPost:
		fr.batchWidgets(w, rest[0], body)
	default:
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "unhandled reporting route "+m+" "+r.URL.Path)
	}
}

// requestedProperties parses ?properties= (repeated and/or comma-separated).
func requestedProperties(r *http.Request) map[string]bool {
	out := map[string]bool{}
	for _, v := range r.URL.Query()["properties"] {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out[p] = true
			}
		}
	}
	return out
}

// lookup returns the object honoring the archived flag (active objects 404
// with archived=true and vice versa).
func (fr *fakeReporting) lookup(kind, id string, archived bool) *fakeReportingObject {
	o, ok := fr.collection(kind)[id]
	if !ok || o.Archived != archived {
		return nil
	}
	return o
}

func (fr *fakeReporting) get(w http.ResponseWriter, r *http.Request, kind, id string) {
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "id must be an integer: "+id)
		return
	}
	o := fr.lookup(kind, id, r.URL.Query().Get("archived") == "true")
	if o == nil {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", fmt.Sprintf("%s %s not found", kind, id))
		return
	}
	writeJSON(w, http.StatusOK, fr.render(kind, o, requestedProperties(r)))
}

// render produces the API JSON for an object, including the optional
// sections only when requested.
func (fr *fakeReporting) render(kind string, o *fakeReportingObject, props map[string]bool) map[string]any {
	out := map[string]any{
		"id":              o.ID,
		"name":            o.Name,
		"businessUnitId":  o.BusinessUnitID,
		"archived":        o.Archived,
		"createdAt":       o.CreatedAt,
		"createdByUserId": o.CreatedByUserID,
		"updatedAt":       o.UpdatedAt,
		"updatedByUserId": o.UpdatedByUserID,
	}
	if o.Description != nil {
		out["description"] = *o.Description
	}
	if o.OwnerUserID != nil {
		out["ownerUserId"] = *o.OwnerUserID
	}
	if o.Archived {
		out["archivedAt"] = o.ArchivedAt
	}
	if o.LastViewedAt != "" {
		out["lastViewedAt"] = o.LastViewedAt
		out["lastViewedByUserId"] = fakeReportingUserID
	}
	if props["permissions"] {
		perm := map[string]any{"permissionType": o.PermissionType}
		if o.PermissionType == "SPECIFIC" {
			if kind == "dashboards" {
				perm["specificPermissions"] = o.Specific
			} else if len(o.Specific) > 0 {
				perm["specificPermissions"] = o.Specific[0]
			}
		}
		out["permissions"] = perm
	}
	if props["tags"] {
		tags := o.Tags
		if tags == nil {
			tags = []fakeTag{}
		}
		out["tags"] = tags
	}
	if kind == "dashboards" && props["widgets"] {
		widgets := make([]map[string]any, 0, len(o.Widgets))
		for _, wd := range o.Widgets {
			widgets = append(widgets, map[string]any{
				"reportId": wd.ReportID,
				"widgetLayout": map[string]any{
					"x": wd.X, "y": wd.Y, "width": wd.Width, "height": wd.Height,
				},
			})
		}
		out["widgets"] = widgets
	}
	return out
}

// decodePermissions validates and stores a permissions object.
func decodePermissions(kind string, raw json.RawMessage) (string, []fakeSpecificPermission, string) {
	var p struct {
		PermissionType      string          `json:"permissionType"`
		SpecificPermissions json.RawMessage `json:"specificPermissions"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", nil, "permissions: " + err.Error()
	}
	switch p.PermissionType {
	case "PRIVATE", "EVERYONE_VIEW", "EVERYONE_EDIT":
		return p.PermissionType, nil, "" // specificPermissions ignored otherwise
	case "SPECIFIC":
	default:
		return "", nil, "permissions.permissionType is invalid: " + p.PermissionType
	}
	if len(p.SpecificPermissions) == 0 || string(p.SpecificPermissions) == "null" {
		return "", nil, "permissions.specificPermissions must be present when permissionType is SPECIFIC"
	}
	var specific []fakeSpecificPermission
	if kind == "dashboards" {
		if err := json.Unmarshal(p.SpecificPermissions, &specific); err != nil {
			return "", nil, "dashboard permissions.specificPermissions must be an array: " + err.Error()
		}
	} else {
		var one fakeSpecificPermission
		if err := json.Unmarshal(p.SpecificPermissions, &one); err != nil {
			return "", nil, "report permissions.specificPermissions must be an object: " + err.Error()
		}
		specific = []fakeSpecificPermission{one}
	}
	// A grantee cannot appear in both the VIEW and the EDIT configuration.
	seen := map[fakeGrant]string{}
	for _, sp := range specific {
		for _, g := range sp.Grants {
			if lvl, dup := seen[g]; dup && lvl != sp.PermissionType {
				return "", nil, "a user or team cannot appear in both VIEW and EDIT specificPermissions"
			}
			seen[g] = sp.PermissionType
		}
	}
	for _, sp := range specific {
		if sp.PermissionType != "VIEW" && sp.PermissionType != "EDIT" {
			return "", nil, "specificPermissions.permissionType must be VIEW or EDIT"
		}
		if len(sp.Grants) == 0 {
			return "", nil, "specificPermissions.grants requires at least one grantee"
		}
		for _, g := range sp.Grants {
			if g.GrantType != "USER" && g.GrantType != "TEAM" {
				return "", nil, "grantType must be USER or TEAM"
			}
		}
	}
	return "SPECIFIC", specific, ""
}

// addWidget appends a widget with a server-assigned layout (two columns of
// 6x4 tiles), idempotently.
func addWidget(o *fakeReportingObject, reportID string) {
	for _, wd := range o.Widgets {
		if wd.ReportID == reportID {
			return
		}
	}
	n := int64(len(o.Widgets))
	o.Widgets = append(o.Widgets, fakeWidget{ReportID: reportID, X: (n % 2) * 6, Y: (n / 2) * 4, Width: 6, Height: 4})
}

func (fr *fakeReporting) createDashboard(w http.ResponseWriter, body []byte) {
	var in struct {
		Name           string          `json:"name"`
		Description    *string         `json:"description"`
		BusinessUnitID *string         `json:"businessUnitId"`
		Permissions    json.RawMessage `json:"permissions"`
		ReportIDsToAdd []string        `json:"reportIdsToAdd"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	if in.Name == "" || len(in.Permissions) == 0 {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "name and permissions are required")
		return
	}
	permType, specific, msg := decodePermissions("dashboards", in.Permissions)
	if msg != "" {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", msg)
		return
	}
	now := fr.tick()
	owner := fakeReportingUserID
	o := &fakeReportingObject{
		ID: fr.newID(), Name: in.Name, Description: in.Description, BusinessUnitID: "0", OwnerUserID: &owner,
		CreatedAt: now, CreatedByUserID: fakeReportingUserID, UpdatedAt: now, UpdatedByUserID: fakeReportingUserID,
		PermissionType: permType, Specific: specific,
	}
	if in.BusinessUnitID != nil {
		o.BusinessUnitID = *in.BusinessUnitID
	}
	// Best effort: unknown / archived report IDs are skipped silently.
	for _, rid := range in.ReportIDsToAdd {
		if fr.lookup("reports", rid, false) != nil {
			addWidget(o, rid)
		}
	}
	fr.dashboards[o.ID] = o
	writeJSON(w, http.StatusOK, fr.render("dashboards", o, map[string]bool{}))
}

func (fr *fakeReporting) patch(w http.ResponseWriter, kind, id string, body []byte) {
	o := fr.lookup(kind, id, false)
	archivedCopy := false
	if o == nil {
		// Restoring (archived:false) addresses an archived object.
		o = fr.lookup(kind, id, true)
		archivedCopy = true
	}
	if o == nil {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", fmt.Sprintf("%s %s not found", kind, id))
		return
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(body, &in); err != nil {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	if raw, ok := in["archived"]; ok {
		if len(in) > 1 {
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "archived cannot be combined with any other fields")
			return
		}
		var archived bool
		_ = json.Unmarshal(raw, &archived)
		fr.setArchived(kind, o, archived)
		writeJSON(w, http.StatusOK, fr.render(kind, o, map[string]bool{}))
		return
	}
	if archivedCopy {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", fmt.Sprintf("%s %s not found", kind, id))
		return
	}
	for k, raw := range in {
		isNull := string(raw) == "null"
		switch k {
		case "name":
			var s string
			if json.Unmarshal(raw, &s) != nil || s == "" {
				writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "name must be a non-empty string")
				return
			}
			o.Name = s
		case "description":
			if isNull {
				o.Description = nil
				continue
			}
			var s string
			_ = json.Unmarshal(raw, &s)
			o.Description = &s
		case "businessUnitId":
			if isNull {
				o.BusinessUnitID = "0"
				continue
			}
			var s string
			_ = json.Unmarshal(raw, &s)
			o.BusinessUnitID = s
		case "ownerUserId":
			var s string
			_ = json.Unmarshal(raw, &s)
			o.OwnerUserID = &s
		case "permissions":
			permType, specific, msg := decodePermissions(kind, raw)
			if msg != "" {
				writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", msg)
				return
			}
			o.PermissionType, o.Specific = permType, specific
		default:
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "unknown field "+k)
			return
		}
	}
	o.UpdatedAt = fr.tick()
	o.UpdatedByUserID = fakeReportingUserID
	writeJSON(w, http.StatusOK, fr.render(kind, o, map[string]bool{}))
}

func (fr *fakeReporting) setArchived(kind string, o *fakeReportingObject, archived bool) {
	o.Archived = archived
	o.UpdatedAt = fr.tick()
	if archived {
		o.ArchivedAt = o.UpdatedAt
		if kind == "reports" {
			// An archived report drops off every dashboard.
			for _, d := range fr.dashboards {
				removeWidget(d, o.ID)
			}
		}
	} else {
		o.ArchivedAt = ""
	}
}

func removeWidget(o *fakeReportingObject, reportID string) bool {
	for i, wd := range o.Widgets {
		if wd.ReportID == reportID {
			o.Widgets = append(o.Widgets[:i], o.Widgets[i+1:]...)
			return true
		}
	}
	return false
}

func (fr *fakeReporting) batchArchive(w http.ResponseWriter, kind string, body []byte, archive bool) {
	var in struct {
		Inputs []string `json:"inputs"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	started := ""
	if !archive {
		started = fr.tick()
	}
	results := []map[string]any{} // never null: `results` is a required array
	for _, id := range in.Inputs {
		if o := fr.lookup(kind, id, !archive); o != nil {
			fr.setArchived(kind, o, archive)
			results = append(results, fr.render(kind, o, map[string]bool{}))
		}
	}
	if archive {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// BatchResponsePublic{Dashboard,Report} requires startedAt/completedAt.
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "COMPLETE", "results": results,
		"requestedAt": started, "startedAt": started, "completedAt": fr.tick(),
	})
}

func (fr *fakeReporting) clone(w http.ResponseWriter, kind, id string, body []byte) {
	src := fr.lookup(kind, id, false)
	if src == nil {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", fmt.Sprintf("%s %s not found", kind, id))
		return
	}
	var in struct {
		Name         string          `json:"name"`
		Permissions  json.RawMessage `json:"permissions"`
		CloneReports *bool           `json:"cloneReports"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	if in.Name == "" || len(in.Permissions) == 0 || (kind == "dashboards" && in.CloneReports == nil) {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "missing required clone fields")
		return
	}
	permType, specific, msg := decodePermissions(kind, in.Permissions)
	if msg != "" {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", msg)
		return
	}
	now := fr.tick()
	owner := fakeReportingUserID
	o := &fakeReportingObject{
		ID: fr.newID(), Name: in.Name, Description: src.Description, BusinessUnitID: src.BusinessUnitID,
		OwnerUserID: &owner, CreatedAt: now, CreatedByUserID: fakeReportingUserID, UpdatedAt: now,
		UpdatedByUserID: fakeReportingUserID, PermissionType: permType, Specific: specific,
	}
	if kind == "dashboards" {
		for _, wd := range src.Widgets {
			rid := wd.ReportID
			if *in.CloneReports {
				srcReport := fr.reports[rid]
				c := *srcReport
				c.ID = fr.newID()
				c.CreatedAt, c.UpdatedAt, c.Tags = now, now, nil
				fr.reports[c.ID] = &c
				rid = c.ID
			}
			o.Widgets = append(o.Widgets, fakeWidget{ReportID: rid, X: wd.X, Y: wd.Y, Width: wd.Width, Height: wd.Height})
		}
	}
	fr.collection(kind)[o.ID] = o
	writeJSON(w, http.StatusOK, fr.render(kind, o, map[string]bool{}))
}

func (fr *fakeReporting) widget(w http.ResponseWriter, dashboardID, reportID string, add bool) {
	d := fr.lookup("dashboards", dashboardID, false)
	if d == nil {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "dashboard "+dashboardID+" not found")
		return
	}
	if add {
		if fr.lookup("reports", reportID, false) == nil {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "report "+reportID+" not found")
			return
		}
		addWidget(d, reportID)
	} else if !removeWidget(d, reportID) {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "report "+reportID+" is not on dashboard "+dashboardID)
		return
	}
	d.UpdatedAt = fr.tick()
	writeJSON(w, http.StatusOK, fr.render("dashboards", d, map[string]bool{"widgets": true}))
}

func (fr *fakeReporting) batchWidgets(w http.ResponseWriter, dashboardID string, body []byte) {
	d := fr.lookup("dashboards", dashboardID, false)
	if d == nil {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "dashboard "+dashboardID+" not found")
		return
	}
	var in struct {
		Inputs []string `json:"inputs"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	for _, rid := range in.Inputs {
		if fr.lookup("reports", rid, false) != nil {
			addWidget(d, rid)
		}
	}
	d.UpdatedAt = fr.tick()
	writeJSON(w, http.StatusOK, fr.render("dashboards", d, map[string]bool{"widgets": true}))
}

// search implements GET /dashboards and GET /reports.
func (fr *fakeReporting) search(w http.ResponseWriter, r *http.Request, kind string) {
	q := r.URL.Query()
	multi := func(key string) map[string]bool {
		out := map[string]bool{}
		for _, v := range q[key] {
			for _, p := range strings.Split(v, ",") {
				if p != "" {
					out[p] = true
				}
			}
		}
		return out
	}
	limit := 25
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > 100 {
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "limit must be between 1 and 100")
			return
		}
		limit = n
	}
	archived := q.Get("archived") == "true"
	needle := strings.ToLower(q.Get("q"))
	ids, owners, tags, units := multi("ids"), multi("ownerUserIds"), multi("tagIds"), multi("businessUnitIds")

	var onDashboard map[string]bool
	if kind == "reports" {
		onDashboard = map[string]bool{}
		for _, d := range fr.dashboards {
			if d.Archived {
				continue
			}
			for _, wd := range d.Widgets {
				onDashboard[wd.ReportID] = true
			}
		}
	}

	var matches []*fakeReportingObject
	for _, o := range fr.collection(kind) {
		if o.Archived != archived {
			continue
		}
		if needle != "" {
			desc := ""
			if o.Description != nil {
				desc = *o.Description
			}
			if !strings.Contains(strings.ToLower(o.Name), needle) && !strings.Contains(strings.ToLower(desc), needle) {
				continue
			}
		}
		if len(ids) > 0 && !ids[o.ID] {
			continue
		}
		if len(owners) > 0 && (o.OwnerUserID == nil || !owners[*o.OwnerUserID]) {
			continue
		}
		if len(units) > 0 && !units[o.BusinessUnitID] {
			continue
		}
		if len(tags) > 0 {
			hit := false
			for _, t := range o.Tags {
				hit = hit || tags[t.ID]
			}
			if !hit {
				continue
			}
		}
		if kind == "reports" {
			if dID := q.Get("dashboardId"); dID != "" {
				d := fr.lookup("dashboards", dID, false)
				on := false
				if d != nil {
					for _, wd := range d.Widgets {
						on = on || wd.ReportID == o.ID
					}
				}
				if !on {
					continue
				}
			}
			if od := q.Get("onDashboard"); od != "" && onDashboard[o.ID] != (od == "true") {
				continue
			}
		}
		matches = append(matches, o)
	}
	// Deterministic order: by numeric ID.
	sort.Slice(matches, func(i, j int) bool {
		a, _ := strconv.ParseInt(matches[i].ID, 10, 64)
		b, _ := strconv.ParseInt(matches[j].ID, 10, 64)
		return a < b
	})

	start := 0
	if after := q.Get("after"); after != "" {
		n, err := strconv.Atoi(after)
		if err != nil || n < 0 {
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid after cursor")
			return
		}
		start = n
	}
	if start > len(matches) {
		start = len(matches)
	}
	end := start + limit
	if end > len(matches) {
		end = len(matches)
	}
	props := requestedProperties(r)
	results := make([]map[string]any, 0, end-start)
	for _, o := range matches[start:end] {
		results = append(results, fr.render(kind, o, props))
	}
	resp := map[string]any{"total": len(matches), "results": results}
	if end < len(matches) {
		resp["paging"] = map[string]any{"next": map[string]any{"after": strconv.Itoa(end)}}
	}
	writeJSON(w, http.StatusOK, resp)
}

// reportingBodyField decodes one top-level field of a recorded request body
// (test assertion helper).
func reportingBodyField(body []byte, field string) (json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if json.NewDecoder(bytes.NewReader(body)).Decode(&m) != nil {
		return nil, false
	}
	v, ok := m[field]
	return v, ok
}
