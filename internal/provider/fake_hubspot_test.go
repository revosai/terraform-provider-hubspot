// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider_test

// A stateful in-memory fake of the HubSpot Properties v3 API (property
// groups + properties), sufficient to run full Terraform lifecycles
// hermetically. It emulates the HubSpot behaviors the provider must handle:
//   - bearer-token auth (401 without it),
//   - archive-not-delete on properties (DELETE archives; GET 404s unless
//     ?archived=true),
//   - "name purgatory": creating a property whose name matches an archived
//     property fails with 400 until the archived one is purged,
//   - 409 on duplicate create,
//   - server-side normalization: option displayOrder is rewritten to the
//     list index, property displayOrder defaults to -1.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type fakeGroup struct {
	Name         string `json:"name"`
	Label        string `json:"label"`
	DisplayOrder int64  `json:"displayOrder"`
	Archived     bool   `json:"archived"`
}

type fakeOption struct {
	Label        string `json:"label"`
	Value        string `json:"value"`
	Description  string `json:"description,omitempty"`
	DisplayOrder int64  `json:"displayOrder"`
	Hidden       bool   `json:"hidden"`
}

type fakeProperty struct {
	Name           string       `json:"name"`
	Label          string       `json:"label"`
	Type           string       `json:"type"`
	FieldType      string       `json:"fieldType"`
	GroupName      string       `json:"groupName"`
	Description    string       `json:"description,omitempty"`
	DisplayOrder   int64        `json:"displayOrder"`
	Hidden         bool         `json:"hidden"`
	FormField      bool         `json:"formField"`
	HasUniqueValue bool         `json:"hasUniqueValue"`
	Options        []fakeOption `json:"options"`
	Archived       bool         `json:"archived"`
	HubspotDefined bool         `json:"hubspotDefined"`
	Calculated     bool         `json:"calculated"`
}

type fakeOwner struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	UserID    int64  `json:"userId"`
	Archived  bool   `json:"archived"`
}

// fakeStage is one stage of a pipeline as stored/returned by the fake.
type fakeStage struct {
	ID           string            `json:"id"`
	Label        string            `json:"label"`
	DisplayOrder int64             `json:"displayOrder"`
	Metadata     map[string]string `json:"metadata"`
}

// fakePipeline is a pipeline as stored/returned by the fake.
type fakePipeline struct {
	ID           string      `json:"id"`
	Label        string      `json:"label"`
	DisplayOrder int64       `json:"displayOrder"`
	Stages       []fakeStage `json:"stages"`
	Default      bool        `json:"-"` // non-deletable default pipeline
}

// fakeStageInput is the wire shape the fake decodes from create/update
// requests: it honors a client-pinned stageId (or id) when present.
type fakeStageInput struct {
	ID           string            `json:"id"`
	StageID      string            `json:"stageId"`
	Label        string            `json:"label"`
	DisplayOrder int64             `json:"displayOrder"`
	Metadata     map[string]string `json:"metadata"`
}

// fakePipelineInput is the wire shape the fake decodes from create/update.
type fakePipelineInput struct {
	Label        string           `json:"label"`
	DisplayOrder int64            `json:"displayOrder"`
	Stages       []fakeStageInput `json:"stages"`
}

// fakeObjectSchema is a custom object schema as stored/returned by the fake.
type fakeObjectSchema struct {
	ID                         string               `json:"id"`
	ObjectTypeID               string               `json:"objectTypeId"`
	FullyQualifiedName         string               `json:"fullyQualifiedName"`
	Name                       string               `json:"name"`
	Labels                     fakeSchemaLabels     `json:"labels"`
	PrimaryDisplayProperty     string               `json:"primaryDisplayProperty"`
	SecondaryDisplayProperties []string             `json:"secondaryDisplayProperties"`
	RequiredProperties         []string             `json:"requiredProperties"`
	SearchableProperties       []string             `json:"searchableProperties"`
	Description                string               `json:"description,omitempty"`
	Properties                 []fakeSchemaProperty `json:"properties"`
	Archived                   bool                 `json:"archived"`
	UpdatedAt                  string               `json:"updatedAt"`
}

// clone deep-copies a schema so a stale snapshot is immune to later writes.
func (s *fakeObjectSchema) clone() *fakeObjectSchema {
	c := *s
	c.SecondaryDisplayProperties = slices.Clone(s.SecondaryDisplayProperties)
	c.RequiredProperties = slices.Clone(s.RequiredProperties)
	c.SearchableProperties = slices.Clone(s.SearchableProperties)
	c.Properties = slices.Clone(s.Properties)
	return &c
}

type fakeSchemaLabels struct {
	Singular string `json:"singular"`
	Plural   string `json:"plural"`
}

// fakeSchemaProperty is a bootstrap property carried on a schema; the fake
// stores what the client sent and echoes it back on the created schema.
type fakeSchemaProperty struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	Type      string `json:"type"`
	FieldType string `json:"fieldType"`
}

// fakeSchemaInput is the create body the fake decodes for POST /crm/v3/schemas.
type fakeSchemaInput struct {
	Name                       string               `json:"name"`
	Labels                     fakeSchemaLabels     `json:"labels"`
	PrimaryDisplayProperty     string               `json:"primaryDisplayProperty"`
	SecondaryDisplayProperties []string             `json:"secondaryDisplayProperties"`
	RequiredProperties         []string             `json:"requiredProperties"`
	SearchableProperties       []string             `json:"searchableProperties"`
	Description                string               `json:"description"`
	Properties                 []fakeSchemaProperty `json:"properties"`
	AssociatedObjects          []string             `json:"associatedObjects"`
}

// fakeAssocLabel is one association label definition as stored/returned by the
// fake Associations v4 API. The wire response includes only category, typeId
// and label — HubSpot never echoes back the `name` sent at creation, so Name
// and InverseTypeID are internal bookkeeping (json:"-") the provider cannot
// read back.
type fakeAssocLabel struct {
	Category      string `json:"category"`
	TypeID        int64  `json:"typeId"`
	Label         string `json:"label"`
	Name          string `json:"-"` // create-time identifier; never serialized
	InverseTypeID int64  `json:"-"` // reverse-pair typeId (0 = unpaired)
}

// fakeLabelInput is the POST create body.
type fakeLabelInput struct {
	Label        string `json:"label"`
	Name         string `json:"name"`
	InverseLabel string `json:"inverseLabel"`
}

// fakeLabelUpdate is the PUT update body.
type fakeLabelUpdate struct {
	AssociationTypeID int64  `json:"associationTypeId"`
	Label             string `json:"label"`
	InverseLabel      string `json:"inverseLabel"`
}

// fakeList is a CRM list as stored by the fake. FilterBranch is stored
// normalized (server defaults injected) and only echoed when includeFilters is
// requested, mirroring the Lists v3 API.
type fakeList struct {
	ListID         string
	Name           string
	ObjectTypeID   string
	ProcessingType string
	FilterBranch   json.RawMessage // normalized; empty for MANUAL
	Archived       bool
}

// fakeListInput is the POST create body.
type fakeListInput struct {
	Name           string          `json:"name"`
	ObjectTypeID   string          `json:"objectTypeId"`
	ProcessingType string          `json:"processingType"`
	FilterBranch   json.RawMessage `json:"filterBranch"`
}

// fakeFlow is an Automation v4 workflow as stored by the fake. The typed
// fields are the ones HubSpot manages at the top level; Extra holds the rest
// of the flow graph (actions, enrollmentCriteria, …) normalized with
// server-injected defaults, mirroring the live beta API.
type fakeFlow struct {
	ID           string
	RevisionID   int64
	Name         string
	Type         string // CONTACT_FLOW / PLATFORM_FLOW
	ObjectTypeID string
	IsEnabled    bool
	Extra        map[string]any
}

type fakeHubSpot struct {
	mu              sync.Mutex
	groups          map[string]map[string]*fakeGroup    // objectType -> name -> group
	properties      map[string]map[string]*fakeProperty // objectType -> name -> property
	pipelines       map[string]map[string]*fakePipeline // objectType -> pipelineId -> pipeline
	schemas         map[string]*fakeObjectSchema        // objectTypeId -> schema
	labels          map[string][]*fakeAssocLabel        // "from/to" -> labels
	lists           map[string]*fakeList                // listId -> list
	flows           map[string]*fakeFlow                // flowId -> flow
	owners          []*fakeOwner
	portalID        int64
	pipelineCounter int
	stageCounter    int
	schemaCounter   int
	labelCounter    int64
	listCounter     int
	flowCounter     int
	lastPipelinePut string // raw query string of the most recent pipeline PUT
	lastFlowPut     []byte // raw body of the most recent flow PUT

	// schemaReadLag emulates HubSpot's stale schema-read cache: after every
	// schema write, GETs alternate between a pre-write snapshot and the
	// current schema until schemaReadLag stale reads have been served
	// (0 = reads are consistent). Creates snapshot the schema *without* its
	// display/required metadata, mirroring HubSpot applying those
	// asynchronously after POST.
	schemaReadLag int
	schemaStale   map[string]*fakeSchemaStale // objectTypeId -> stale generation
	schemaWrites  int                         // monotonic updatedAt source
}

// fakeSchemaStale is a pre-write cache generation served interleaved with
// fresh reads, mirroring the live API's load-balanced stale cache.
type fakeSchemaStale struct {
	snapshot *fakeObjectSchema
	reads    int // stale reads remaining
	served   int // reads since the write, to alternate stale/fresh
}

func newFakeHubSpot(t *testing.T) (*fakeHubSpot, *httptest.Server) {
	t.Helper()
	f := &fakeHubSpot{
		groups:      map[string]map[string]*fakeGroup{},
		properties:  map[string]map[string]*fakeProperty{},
		pipelines:   map[string]map[string]*fakePipeline{},
		schemas:     map[string]*fakeObjectSchema{},
		labels:      map[string][]*fakeAssocLabel{},
		lists:       map[string]*fakeList{},
		flows:       map[string]*fakeFlow{},
		schemaStale: map[string]*fakeSchemaStale{},
		portalID:    123456,
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

// seedOwner registers an owner so the hubspot_owner data source can find it.
func (f *fakeHubSpot) seedOwner(o fakeOwner) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owner := o
	f.owners = append(f.owners, &owner)
}

// deleteProperty simulates out-of-band deletion (for _disappears tests).
func (f *fakeHubSpot) deleteProperty(objectType, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.properties[objectType], name)
}

// deleteGroup simulates out-of-band deletion (for _disappears tests).
func (f *fakeHubSpot) deleteGroup(objectType, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.groups[objectType], name)
}

// deletePipeline simulates out-of-band deletion (for _disappears tests).
func (f *fakeHubSpot) deletePipeline(objectType, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pipelines[objectType], id)
}

// seedDefaultPipeline installs a pre-existing non-deletable "default" pipeline
// (like HubSpot's built-in deal pipeline) so tests can adopt it via import and
// assert that DELETE is rejected with a "default" error.
func (f *fakeHubSpot) seedDefaultPipeline(objectType, id, label string, stages []fakeStage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pipelines[objectType] == nil {
		f.pipelines[objectType] = map[string]*fakePipeline{}
	}
	f.pipelines[objectType][id] = &fakePipeline{
		ID:      id,
		Label:   label,
		Stages:  stages,
		Default: true,
	}
}

// lastPipelinePutQuery returns the raw query string of the most recent pipeline
// PUT, so update tests can assert the delete-guard params were transmitted.
func (f *fakeHubSpot) lastPipelinePutQuery() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastPipelinePut
}

func (f *fakeHubSpot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeHubSpotError(w, http.StatusUnauthorized, "AUTHENTICATION_FAILED", "missing bearer token")
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")

	f.mu.Lock()
	defer f.mu.Unlock()

	// account-info/v3/details — portal identity (data source hubspot_portal).
	if len(parts) == 3 && parts[0] == "account-info" && parts[1] == "v3" && parts[2] == "details" {
		f.accountInfo(w, r)
		return
	}
	// crm/v3/owners[/{ownerId}] — read-only (data source hubspot_owner).
	if len(parts) >= 3 && parts[0] == "crm" && parts[1] == "v3" && parts[2] == "owners" {
		f.ownersRoute(w, r, parts[3:])
		return
	}
	// crm/v3/pipelines/{objectType}[/{pipelineId}] — resource hubspot_pipeline.
	if len(parts) >= 3 && parts[0] == "crm" && parts[1] == "v3" && parts[2] == "pipelines" {
		f.pipelinesRoute(w, r, parts[3:])
		return
	}
	// crm/v3/schemas[/{objectType}] — resource hubspot_object_schema.
	if len(parts) >= 3 && parts[0] == "crm" && parts[1] == "v3" && parts[2] == "schemas" {
		f.schemasRoute(w, r, parts[3:])
		return
	}
	// crm/v4/associations/{from}/{to}/labels[/{typeId}] — resource
	// hubspot_association_label.
	if len(parts) >= 6 && parts[0] == "crm" && parts[1] == "v4" && parts[2] == "associations" && parts[5] == "labels" {
		f.associationLabelsRoute(w, r, parts[3], parts[4], parts[6:])
		return
	}
	// crm/v3/lists[/...] — resource hubspot_list.
	if len(parts) >= 3 && parts[0] == "crm" && parts[1] == "v3" && parts[2] == "lists" {
		f.listsRoute(w, r, parts[3:])
		return
	}
	// automation/v4/flows[/{flowId}] — resource hubspot_workflow.
	if len(parts) >= 3 && parts[0] == "automation" && parts[1] == "v4" && parts[2] == "flows" {
		f.flowsRoute(w, r, parts[3:])
		return
	}

	// Expected property shapes:
	//   crm/v3/properties/{objectType}
	//   crm/v3/properties/{objectType}/{propertyName}
	//   crm/v3/properties/{objectType}/groups
	//   crm/v3/properties/{objectType}/groups/{groupName}
	if len(parts) < 4 || parts[0] != "crm" || parts[1] != "v3" || parts[2] != "properties" {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "unknown path "+r.URL.Path)
		return
	}
	objectType := parts[3]
	rest := parts[4:]

	switch {
	case len(rest) == 1 && rest[0] == "groups" && r.Method == http.MethodPost:
		f.createGroup(w, r, objectType)
	case len(rest) == 2 && rest[0] == "groups":
		f.groupByName(w, r, objectType, rest[1])
	case len(rest) == 0 && r.Method == http.MethodPost:
		f.createProperty(w, r, objectType)
	case len(rest) == 0 && r.Method == http.MethodGet:
		f.listProperties(w, r, objectType)
	case len(rest) == 1 && rest[0] != "groups":
		f.propertyByName(w, r, objectType, rest[0])
	default:
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "unhandled route "+r.Method+" "+r.URL.Path)
	}
}

func (f *fakeHubSpot) createGroup(w http.ResponseWriter, r *http.Request, objectType string) {
	var in fakeGroup
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON: "+err.Error())
		return
	}
	if in.Name == "" || in.Label == "" {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "name and label are required")
		return
	}
	if f.groups[objectType] == nil {
		f.groups[objectType] = map[string]*fakeGroup{}
	}
	if _, exists := f.groups[objectType][in.Name]; exists {
		writeHubSpotError(w, http.StatusConflict, "CONFLICT", "property group "+in.Name+" already exists")
		return
	}
	g := in
	f.groups[objectType][in.Name] = &g
	writeJSON(w, http.StatusCreated, g)
}

func (f *fakeHubSpot) groupByName(w http.ResponseWriter, r *http.Request, objectType, name string) {
	g := f.groups[objectType][name]
	switch r.Method {
	case http.MethodGet:
		if g == nil {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "group not found")
			return
		}
		writeJSON(w, http.StatusOK, g)
	case http.MethodPatch:
		if g == nil {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "group not found")
			return
		}
		var patch map[string]any
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON")
			return
		}
		if v, ok := patch["label"].(string); ok {
			g.Label = v
		}
		if v, ok := patch["displayOrder"].(float64); ok {
			g.DisplayOrder = int64(v)
		}
		writeJSON(w, http.StatusOK, g)
	case http.MethodDelete:
		if g == nil {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "group not found")
			return
		}
		delete(f.groups[objectType], name)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeHubSpotError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", r.Method)
	}
}

func (f *fakeHubSpot) createProperty(w http.ResponseWriter, r *http.Request, objectType string) {
	var in fakeProperty
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON: "+err.Error())
		return
	}
	if in.Name == "" || in.Label == "" || in.Type == "" || in.FieldType == "" || in.GroupName == "" {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "name, label, type, fieldType and groupName are required")
		return
	}
	if f.properties[objectType] == nil {
		f.properties[objectType] = map[string]*fakeProperty{}
	}
	if existing, ok := f.properties[objectType][in.Name]; ok {
		if existing.Archived {
			// Name purgatory: archived property blocks the name until purged.
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR",
				"a property with this name was recently archived; the name cannot be reused until it is purged")
			return
		}
		writeHubSpotError(w, http.StatusConflict, "CONFLICT", "property "+in.Name+" already exists")
		return
	}
	p := in
	normalizeProperty(&p)
	f.properties[objectType][in.Name] = &p
	writeJSON(w, http.StatusCreated, p)
}

func (f *fakeHubSpot) propertyByName(w http.ResponseWriter, r *http.Request, objectType, name string) {
	p := f.properties[objectType][name]
	wantArchived := r.URL.Query().Get("archived") == "true"
	switch r.Method {
	case http.MethodGet:
		if p == nil || p.Archived != wantArchived {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "property not found")
			return
		}
		writeJSON(w, http.StatusOK, p)
	case http.MethodPatch:
		if p == nil || p.Archived {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "property not found")
			return
		}
		var patch fakeProperty
		raw, _ := json.Marshal(p)
		_ = json.Unmarshal(raw, &patch) // start from current values
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON")
			return
		}
		if patch.Name != p.Name || patch.Type != p.Type {
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "name and type cannot be changed")
			return
		}
		normalizeProperty(&patch)
		*p = patch
		writeJSON(w, http.StatusOK, p)
	case http.MethodDelete:
		if p == nil || p.Archived {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "property not found")
			return
		}
		p.Archived = true // archive, not delete
		w.WriteHeader(http.StatusNoContent)
	default:
		writeHubSpotError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", r.Method)
	}
}

// listProperties emulates GET /crm/v3/properties/{objectType}. Like real
// HubSpot, archived properties are excluded unless archived=true, and the
// results are returned under a "results" envelope (no pagination for the
// sizes used in tests).
func (f *fakeHubSpot) listProperties(w http.ResponseWriter, r *http.Request, objectType string) {
	wantArchived := r.URL.Query().Get("archived") == "true"
	results := make([]*fakeProperty, 0, len(f.properties[objectType]))
	for _, p := range f.properties[objectType] {
		if p.Archived != wantArchived {
			continue
		}
		results = append(results, p)
	}
	// Deterministic order by name so tests don't flake on map iteration order.
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// schemasRoute dispatches /crm/v3/schemas[/{objectType}].
func (f *fakeHubSpot) schemasRoute(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodPost:
		f.createSchema(w, r)
	case len(rest) == 1:
		f.schemaByType(w, r, rest[0])
	default:
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "unhandled route "+r.Method+" "+r.URL.Path)
	}
}

// lookupSchema resolves an object-type reference (objectTypeId,
// fullyQualifiedName, or bare name) to a stored schema — like real HubSpot,
// which accepts any of the three in the path.
func (f *fakeHubSpot) lookupSchema(ref string) *fakeObjectSchema {
	if s, ok := f.schemas[ref]; ok {
		return s
	}
	for _, s := range f.schemas {
		if s.FullyQualifiedName == ref || s.Name == ref {
			return s
		}
	}
	return nil
}

func (f *fakeHubSpot) createSchema(w http.ResponseWriter, r *http.Request) {
	var in fakeSchemaInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON: "+err.Error())
		return
	}
	if in.Name == "" || in.Labels.Singular == "" || in.Labels.Plural == "" {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "name and labels.singular/plural are required")
		return
	}
	if len(in.Properties) == 0 {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "at least one property is required")
		return
	}
	if in.PrimaryDisplayProperty != "" {
		found := false
		for _, p := range in.Properties {
			if p.Name == in.PrimaryDisplayProperty {
				found = true
				break
			}
		}
		if !found {
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR",
				"primaryDisplayProperty must be one of the supplied properties")
			return
		}
	}
	// Reject a duplicate name (matches HubSpot's 409 on re-create).
	if f.lookupSchema(in.Name) != nil {
		writeHubSpotError(w, http.StatusConflict, "CONFLICT", "object schema "+in.Name+" already exists")
		return
	}

	f.schemaCounter++
	objectTypeID := fmt.Sprintf("2-%d", f.schemaCounter)
	s := &fakeObjectSchema{
		ID:                         fmt.Sprintf("%d", f.schemaCounter),
		ObjectTypeID:               objectTypeID,
		FullyQualifiedName:         fmt.Sprintf("p%d_%s", f.portalID, in.Name),
		Name:                       in.Name,
		Labels:                     in.Labels,
		PrimaryDisplayProperty:     in.PrimaryDisplayProperty,
		SecondaryDisplayProperties: in.SecondaryDisplayProperties,
		RequiredProperties:         in.RequiredProperties,
		SearchableProperties:       in.SearchableProperties,
		Description:                in.Description,
		Properties:                 in.Properties,
	}
	s.normalizeSearchable()
	f.stampSchemaWrite(s)
	f.schemas[objectTypeID] = s

	// The live API applies primaryDisplayProperty/requiredProperties
	// asynchronously after POST: stale cache generations serve the schema
	// with those defaulted for a while. Stage that pre-metadata snapshot.
	if f.schemaReadLag > 0 {
		stale := s.clone()
		stale.PrimaryDisplayProperty = "hs_object_id"
		stale.RequiredProperties = nil
		stale.SearchableProperties = []string{"hs_object_id"}
		stale.UpdatedAt = "" // pre-write generation sorts older
		f.schemaStale[objectTypeID] = &fakeSchemaStale{snapshot: stale, reads: f.schemaReadLag}
	}
	writeJSON(w, http.StatusCreated, s)
}

// stampSchemaWrite advances the schema's updatedAt; the counter keeps stamps
// monotonic so fresher generations always compare greater.
func (f *fakeHubSpot) stampSchemaWrite(s *fakeObjectSchema) {
	f.schemaWrites++
	s.UpdatedAt = fmt.Sprintf("2026-01-01T00:00:00.%09dZ", f.schemaWrites)
}

// stageStaleSchema snapshots the current schema as a stale cache generation
// served interleaved with fresh reads for the next schemaReadLag stale GETs.
// An already-pending older generation is kept (oldest wins, like a cache node
// that missed several writes).
func (f *fakeHubSpot) stageStaleSchema(s *fakeObjectSchema) {
	if f.schemaReadLag <= 0 {
		return
	}
	if _, ok := f.schemaStale[s.ObjectTypeID]; ok {
		return
	}
	f.schemaStale[s.ObjectTypeID] = &fakeSchemaStale{snapshot: s.clone(), reads: f.schemaReadLag}
}

// normalizeSearchable emulates HubSpot always indexing the primary display
// property for search: the server injects it into searchableProperties on
// every create/update, whether or not the request listed it.
func (s *fakeObjectSchema) normalizeSearchable() {
	if s.PrimaryDisplayProperty == "" {
		return
	}
	if !slices.Contains(s.SearchableProperties, s.PrimaryDisplayProperty) {
		s.SearchableProperties = append(s.SearchableProperties, s.PrimaryDisplayProperty)
	}
}

func (f *fakeHubSpot) schemaByType(w http.ResponseWriter, r *http.Request, ref string) {
	s := f.lookupSchema(ref)
	switch r.Method {
	case http.MethodGet:
		if s == nil {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "schema not found")
			return
		}
		// Serve a pending stale generation on alternating reads, mirroring
		// the live API's load-balanced cache (first read after a write is
		// stale — observed live).
		if e, ok := f.schemaStale[s.ObjectTypeID]; ok {
			e.served++
			if e.served%2 == 1 {
				e.reads--
				if e.reads <= 0 {
					delete(f.schemaStale, s.ObjectTypeID)
				}
				writeJSON(w, http.StatusOK, e.snapshot)
				return
			}
		}
		writeJSON(w, http.StatusOK, s)
	case http.MethodPatch:
		if s == nil {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "schema not found")
			return
		}
		var patch struct {
			Labels                     *fakeSchemaLabels `json:"labels"`
			PrimaryDisplayProperty     *string           `json:"primaryDisplayProperty"`
			SecondaryDisplayProperties *[]string         `json:"secondaryDisplayProperties"`
			RequiredProperties         *[]string         `json:"requiredProperties"`
			SearchableProperties       *[]string         `json:"searchableProperties"`
			Description                *string           `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON")
			return
		}
		f.stageStaleSchema(s)
		if patch.Labels != nil {
			s.Labels = *patch.Labels
		}
		if patch.PrimaryDisplayProperty != nil {
			s.PrimaryDisplayProperty = *patch.PrimaryDisplayProperty
		}
		if patch.SecondaryDisplayProperties != nil {
			s.SecondaryDisplayProperties = *patch.SecondaryDisplayProperties
		}
		if patch.RequiredProperties != nil {
			s.RequiredProperties = *patch.RequiredProperties
		}
		if patch.SearchableProperties != nil {
			s.SearchableProperties = *patch.SearchableProperties
		}
		if patch.Description != nil {
			s.Description = *patch.Description
		}
		s.normalizeSearchable()
		f.stampSchemaWrite(s)
		writeJSON(w, http.StatusOK, s)
	case http.MethodDelete:
		if s == nil {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "schema not found")
			return
		}
		if r.URL.Query().Get("archived") == "true" {
			// Hard delete (purge). HubSpot only permits this once the schema is
			// archived and holds zero records; the fake has no records.
			delete(f.schemas, s.ObjectTypeID)
			delete(f.schemaStale, s.ObjectTypeID)
		} else {
			f.stageStaleSchema(s)
			s.Archived = true // soft delete
			f.stampSchemaWrite(s)
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeHubSpotError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", r.Method)
	}
}

// deleteSchema simulates out-of-band deletion (for _disappears tests).
func (f *fakeHubSpot) deleteSchema(objectTypeID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.schemas, objectTypeID)
}

// normalizeProperty emulates HubSpot server-side normalization.
func normalizeProperty(p *fakeProperty) {
	for i := range p.Options {
		p.Options[i].DisplayOrder = int64(i)
	}
	if p.DisplayOrder == 0 {
		p.DisplayOrder = -1
	}
}

// accountInfo emulates GET /account-info/v3/details.
func (f *fakeHubSpot) accountInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeHubSpotError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", r.Method)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"portalId":            f.portalID,
		"accountType":         "STANDARD",
		"timeZone":            "US/Eastern",
		"companyCurrency":     "USD",
		"uiDomain":            "app.hubspot.com",
		"dataHostingLocation": "na1",
	})
}

// ownersRoute emulates GET /crm/v3/owners and /crm/v3/owners/{ownerId}.
func (f *fakeHubSpot) ownersRoute(w http.ResponseWriter, r *http.Request, rest []string) {
	if r.Method != http.MethodGet {
		writeHubSpotError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", r.Method)
		return
	}
	// GET /crm/v3/owners/{ownerId}
	if len(rest) == 1 {
		for _, o := range f.owners {
			if o.ID == rest[0] {
				writeJSON(w, http.StatusOK, o)
				return
			}
		}
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "owner not found")
		return
	}
	// GET /crm/v3/owners?email=  — like real HubSpot, archived owners are
	// excluded unless archived=true is requested.
	emailFilter := r.URL.Query().Get("email")
	wantArchived := r.URL.Query().Get("archived") == "true"
	results := make([]*fakeOwner, 0, len(f.owners))
	for _, o := range f.owners {
		if o.Archived != wantArchived {
			continue
		}
		if emailFilter != "" && o.Email != emailFilter {
			continue
		}
		results = append(results, o)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// pipelinesRoute dispatches /crm/v3/pipelines/{objectType}[/{pipelineId}].
func (f *fakeHubSpot) pipelinesRoute(w http.ResponseWriter, r *http.Request, rest []string) {
	if len(rest) == 0 {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "missing object type")
		return
	}
	objectType := rest[0]
	switch {
	case len(rest) == 1 && r.Method == http.MethodPost:
		f.createPipeline(w, r, objectType)
	case len(rest) == 2:
		f.pipelineByID(w, r, objectType, rest[1])
	default:
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "unhandled route "+r.Method+" "+r.URL.Path)
	}
}

// assignStages converts input stages into stored stages, honoring a
// client-pinned stageId/id and otherwise minting a deterministic "stg_N".
//
// It emulates HubSpot server-side metadata normalization: real Pipelines v3
// always injects an `isClosed` key ("true"/"false") into deal-stage metadata,
// derived from the stage probability, regardless of what the client sent. The
// injection is applied to the RETURNED copy only (never the client-sent map),
// so the provider must tolerate server-injected metadata keys it did not send.
func (f *fakeHubSpot) assignStages(objectType string, in []fakeStageInput) []fakeStage {
	stages := make([]fakeStage, 0, len(in))
	for _, s := range in {
		id := s.StageID
		if id == "" {
			id = s.ID
		}
		if id == "" {
			f.stageCounter++
			id = fmt.Sprintf("stg_%d", f.stageCounter)
		}
		// Copy metadata so injected server keys never leak into the input map.
		var md map[string]string
		if s.Metadata != nil {
			md = make(map[string]string, len(s.Metadata)+1)
			for k, v := range s.Metadata {
				md[k] = v
			}
		}
		if objectType == "deals" {
			if md == nil {
				md = map[string]string{}
			}
			md["isClosed"] = dealStageIsClosed(md["probability"])
		}
		stages = append(stages, fakeStage{
			ID:           id,
			Label:        s.Label,
			DisplayOrder: s.DisplayOrder,
			Metadata:     md,
		})
	}
	return stages
}

// dealStageIsClosed derives the server-injected isClosed flag from a deal
// stage's probability, matching HubSpot's behavior closely enough for tests
// (0.0 and 1.0 are closed stages).
func dealStageIsClosed(probability string) string {
	if probability == "1.0" || probability == "0.0" {
		return "true"
	}
	return "false"
}

func (f *fakeHubSpot) createPipeline(w http.ResponseWriter, r *http.Request, objectType string) {
	var in fakePipelineInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON: "+err.Error())
		return
	}
	if in.Label == "" {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "label is required")
		return
	}
	if len(in.Stages) == 0 {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "at least one stage is required")
		return
	}
	if f.pipelines[objectType] == nil {
		f.pipelines[objectType] = map[string]*fakePipeline{}
	}
	f.pipelineCounter++
	p := &fakePipeline{
		ID:           fmt.Sprintf("pl_%d", f.pipelineCounter),
		Label:        in.Label,
		DisplayOrder: in.DisplayOrder,
		Stages:       f.assignStages(objectType, in.Stages),
	}
	f.pipelines[objectType][p.ID] = p
	writeJSON(w, http.StatusCreated, p)
}

func (f *fakeHubSpot) pipelineByID(w http.ResponseWriter, r *http.Request, objectType, id string) {
	p := f.pipelines[objectType][id]
	switch r.Method {
	case http.MethodGet:
		if p == nil {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "pipeline not found")
			return
		}
		writeJSON(w, http.StatusOK, p)
	case http.MethodPut:
		f.lastPipelinePut = r.URL.RawQuery
		if p == nil {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "pipeline not found")
			return
		}
		var in fakePipelineInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON: "+err.Error())
			return
		}
		if in.Label == "" || len(in.Stages) == 0 {
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "label and at least one stage are required")
			return
		}
		p.Label = in.Label
		p.DisplayOrder = in.DisplayOrder
		p.Stages = f.assignStages(objectType, in.Stages)
		writeJSON(w, http.StatusOK, p)
	case http.MethodDelete:
		if p == nil {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "pipeline not found")
			return
		}
		if p.Default {
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR",
				"the default pipeline cannot be deleted")
			return
		}
		delete(f.pipelines[objectType], id)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeHubSpotError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", r.Method)
	}
}

// seedAssociationLabel installs a pre-existing label (e.g. a HUBSPOT_DEFINED
// one) so import-error tests can adopt it.
func (f *fakeHubSpot) seedAssociationLabel(from, to string, l fakeAssocLabel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	label := l
	f.labels[from+"/"+to] = append(f.labels[from+"/"+to], &label)
}

// deleteAssociationLabelOOB simulates out-of-band deletion (for _disappears
// tests), removing the label (and its paired reverse entry) by typeId.
func (f *fakeHubSpot) deleteAssociationLabelOOB(from, to string, typeID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeLabelLocked(from, to, typeID)
}

// removeLabelLocked drops the label with typeID from from/to and, if it is
// paired, its reverse entry from to/from. Caller holds f.mu.
func (f *fakeHubSpot) removeLabelLocked(from, to string, typeID int64) {
	key := from + "/" + to
	var inverseTypeID int64
	kept := f.labels[key][:0]
	for _, l := range f.labels[key] {
		if l.TypeID == typeID {
			inverseTypeID = l.InverseTypeID
			continue
		}
		kept = append(kept, l)
	}
	f.labels[key] = kept
	if inverseTypeID == 0 {
		return
	}
	rkey := to + "/" + from
	rkept := f.labels[rkey][:0]
	for _, l := range f.labels[rkey] {
		if l.TypeID == inverseTypeID {
			continue
		}
		rkept = append(rkept, l)
	}
	f.labels[rkey] = rkept
}

// associationLabelsRoute dispatches /crm/v4/associations/{from}/{to}/labels
// and .../labels/{typeId}.
func (f *fakeHubSpot) associationLabelsRoute(w http.ResponseWriter, r *http.Request, from, to string, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"results": f.labels[from+"/"+to]})
	case len(rest) == 0 && r.Method == http.MethodPost:
		f.createAssociationLabel(w, r, from, to)
	case len(rest) == 0 && r.Method == http.MethodPut:
		f.updateAssociationLabel(w, r, from, to)
	case len(rest) == 1 && r.Method == http.MethodDelete:
		f.deleteAssociationLabel(w, r, from, to, rest[0])
	default:
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "unhandled route "+r.Method+" "+r.URL.Path)
	}
}

func (f *fakeHubSpot) createAssociationLabel(w http.ResponseWriter, r *http.Request, from, to string) {
	var in fakeLabelInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON: "+err.Error())
		return
	}
	if in.Label == "" {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "label is required")
		return
	}
	f.labelCounter++
	fwdID := f.labelCounter
	fwd := &fakeAssocLabel{Category: "USER_DEFINED", TypeID: fwdID, Label: in.Label, Name: in.Name}
	results := []*fakeAssocLabel{fwd}
	if in.InverseLabel != "" {
		f.labelCounter++
		invID := f.labelCounter
		inv := &fakeAssocLabel{Category: "USER_DEFINED", TypeID: invID, Label: in.InverseLabel, InverseTypeID: fwdID}
		fwd.InverseTypeID = invID
		f.labels[to+"/"+from] = append(f.labels[to+"/"+from], inv)
		results = append(results, inv)
	}
	f.labels[from+"/"+to] = append(f.labels[from+"/"+to], fwd)
	writeJSON(w, http.StatusCreated, map[string]any{"results": results})
}

func (f *fakeHubSpot) updateAssociationLabel(w http.ResponseWriter, r *http.Request, from, to string) {
	var in fakeLabelUpdate
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON: "+err.Error())
		return
	}
	var fwd *fakeAssocLabel
	for _, l := range f.labels[from+"/"+to] {
		if l.TypeID == in.AssociationTypeID {
			fwd = l
			break
		}
	}
	if fwd == nil {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "association label not found")
		return
	}
	if in.Label != "" {
		fwd.Label = in.Label
	}
	if in.InverseLabel != "" && fwd.InverseTypeID != 0 {
		for _, l := range f.labels[to+"/"+from] {
			if l.TypeID == fwd.InverseTypeID {
				l.Label = in.InverseLabel
				break
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeHubSpot) deleteAssociationLabel(w http.ResponseWriter, _ *http.Request, from, to, typeIDStr string) {
	typeID, err := strconv.ParseInt(typeIDStr, 10, 64)
	if err != nil {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid typeId: "+typeIDStr)
		return
	}
	var fwd *fakeAssocLabel
	for _, l := range f.labels[from+"/"+to] {
		if l.TypeID == typeID {
			fwd = l
			break
		}
	}
	if fwd == nil {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "association label not found")
		return
	}
	if fwd.Category == "HUBSPOT_DEFINED" {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR",
			"a HubSpot-defined association label cannot be deleted")
		return
	}
	f.removeLabelLocked(from, to, typeID)
	w.WriteHeader(http.StatusNoContent)
}

// deleteListOOB simulates out-of-band deletion (for _disappears tests).
func (f *fakeHubSpot) deleteListOOB(listID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.lists, listID)
}

// listsRoute dispatches /crm/v3/lists[/{listId}[/{action}]].
func (f *fakeHubSpot) listsRoute(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodPost:
		f.createList(w, r)
	case len(rest) == 1 && r.Method == http.MethodGet:
		f.getList(w, r, rest[0])
	case len(rest) == 1 && r.Method == http.MethodDelete:
		f.deleteList(w, rest[0])
	case len(rest) == 2 && r.Method == http.MethodPut && rest[1] == "update-list-name":
		f.updateListName(w, r, rest[0])
	case len(rest) == 2 && r.Method == http.MethodPut && rest[1] == "update-list-filters":
		f.updateListFilters(w, r, rest[0])
	case len(rest) == 2 && r.Method == http.MethodPut && rest[1] == "restore":
		f.restoreList(w, rest[0])
	default:
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "unhandled route "+r.Method+" "+r.URL.Path)
	}
}

// normalizeFilterBranch emulates HubSpot's server-side expansion of a filter
// tree: it injects filterBranchOperator (= filterBranchType) on every branch
// and includeObjectsWithNoValueSet=false on every filter operation, unless
// already present. Returns the re-marshaled JSON.
func normalizeFilterBranch(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return raw
	}
	injectFilterDefaults(tree)
	out, err := json.Marshal(tree)
	if err != nil {
		return raw
	}
	return out
}

func injectFilterDefaults(node any) {
	m, ok := node.(map[string]any)
	if !ok {
		if arr, ok := node.([]any); ok {
			for _, e := range arr {
				injectFilterDefaults(e)
			}
		}
		return
	}
	if t, ok := m["filterBranchType"]; ok {
		if _, has := m["filterBranchOperator"]; !has {
			m["filterBranchOperator"] = t
		}
	}
	if op, ok := m["operation"].(map[string]any); ok {
		if _, has := op["includeObjectsWithNoValueSet"]; !has {
			op["includeObjectsWithNoValueSet"] = false
		}
	}
	for _, v := range m {
		injectFilterDefaults(v)
	}
}

func (f *fakeHubSpot) createList(w http.ResponseWriter, r *http.Request) {
	var in fakeListInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON: "+err.Error())
		return
	}
	if in.Name == "" || in.ObjectTypeID == "" || in.ProcessingType == "" {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "name, objectTypeId and processingType are required")
		return
	}
	f.listCounter++
	l := &fakeList{
		ListID:         fmt.Sprintf("%d", f.listCounter),
		Name:           in.Name,
		ObjectTypeID:   in.ObjectTypeID,
		ProcessingType: in.ProcessingType,
		FilterBranch:   normalizeFilterBranch(in.FilterBranch),
	}
	f.lists[l.ListID] = l
	// Create response mirrors HubSpot: no filterBranch echoed.
	writeJSON(w, http.StatusCreated, map[string]any{"list": listResponse(l, false)})
}

func (f *fakeHubSpot) getList(w http.ResponseWriter, r *http.Request, listID string) {
	l := f.lists[listID]
	if l == nil || l.Archived {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "list not found")
		return
	}
	includeFilters := r.URL.Query().Get("includeFilters") == "true"
	writeJSON(w, http.StatusOK, map[string]any{"list": listResponse(l, includeFilters)})
}

func (f *fakeHubSpot) updateListName(w http.ResponseWriter, r *http.Request, listID string) {
	l := f.lists[listID]
	if l == nil || l.Archived {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "list not found")
		return
	}
	name := r.URL.Query().Get("listName")
	if name == "" {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "listName query parameter is required")
		return
	}
	l.Name = name
	writeJSON(w, http.StatusOK, map[string]any{"list": listResponse(l, false)})
}

func (f *fakeHubSpot) updateListFilters(w http.ResponseWriter, r *http.Request, listID string) {
	l := f.lists[listID]
	if l == nil || l.Archived {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "list not found")
		return
	}
	var body struct {
		FilterBranch json.RawMessage `json:"filterBranch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON: "+err.Error())
		return
	}
	l.FilterBranch = normalizeFilterBranch(body.FilterBranch)
	writeJSON(w, http.StatusOK, map[string]any{"list": listResponse(l, true)})
}

func (f *fakeHubSpot) deleteList(w http.ResponseWriter, listID string) {
	l := f.lists[listID]
	if l == nil || l.Archived {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "list not found")
		return
	}
	l.Archived = true // archive, not hard delete
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeHubSpot) restoreList(w http.ResponseWriter, listID string) {
	l := f.lists[listID]
	if l == nil {
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "list not found")
		return
	}
	l.Archived = false
	w.WriteHeader(http.StatusNoContent)
}

// listResponse builds the JSON list object, echoing filterBranch only when
// requested (matching includeFilters semantics).
func listResponse(l *fakeList, includeFilters bool) map[string]any {
	out := map[string]any{
		"listId":         l.ListID,
		"name":           l.Name,
		"objectTypeId":   l.ObjectTypeID,
		"processingType": l.ProcessingType,
		"listVersion":    1,
		"createdAt":      "2026-02-02T16:13:48.146Z",
		"updatedAt":      "2026-02-02T16:13:48.146Z",
	}
	if includeFilters && len(l.FilterBranch) > 0 {
		out["filterBranch"] = l.FilterBranch
	}
	return out
}

// seedFlow installs a pre-existing workflow (with an empty action graph) so
// data-source tests can look it up without a Terraform-managed fixture.
func (f *fakeHubSpot) seedFlow(name, flowType, objectTypeID string, enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flowCounter++
	fl := &fakeFlow{
		ID:           fmt.Sprintf("%d", f.flowCounter),
		RevisionID:   1,
		Name:         name,
		Type:         flowType,
		ObjectTypeID: objectTypeID,
		IsEnabled:    enabled,
		Extra:        map[string]any{"actions": []any{}},
	}
	normalizeFlowExtra(fl.Extra)
	f.flows[fl.ID] = fl
}

// deleteFlowOOB simulates out-of-band deletion (for _disappears tests).
func (f *fakeHubSpot) deleteFlowOOB(flowID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.flows, flowID)
}

// bumpFlowRevisionOOB simulates a concurrent edit in the HubSpot UI: the
// flow's revisionId advances without the provider seeing it, so a stale
// PUT would 409. The provider's GET-then-PUT must absorb this.
func (f *fakeHubSpot) bumpFlowRevisionOOB(flowID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fl := f.flows[flowID]; fl != nil {
		fl.RevisionID++
	}
}

// flowRevision returns the flow's current revisionId (0 if missing), so
// tests can assert optimistic-lock bookkeeping.
func (f *fakeHubSpot) flowRevision(flowID string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fl := f.flows[flowID]; fl != nil {
		return fl.RevisionID
	}
	return 0
}

// lastFlowPutBody returns the raw body of the most recent flow PUT, so tests
// can assert the transmitted revisionId.
func (f *fakeHubSpot) lastFlowPutBody() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastFlowPut
}

// flowsRoute dispatches /automation/v4/flows[/{flowId}].
func (f *fakeHubSpot) flowsRoute(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodPost:
		f.createFlow(w, r)
	case len(rest) == 0 && r.Method == http.MethodGet:
		f.listFlows(w, r)
	case len(rest) == 1:
		f.flowByID(w, r, rest[0])
	default:
		writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "unhandled route "+r.Method+" "+r.URL.Path)
	}
}

// flowManagedKeys are the top-level flow fields the fake stores typed (and the
// server-owned identity/audit fields); everything else lands in Extra.
var flowManagedKeys = []string{
	"id", "revisionId", "name", "type", "objectTypeId", "isEnabled",
	"flowType", "createdAt", "updatedAt",
}

// decodeFlowBody splits a create/update body into the typed fields and the
// normalized Extra remainder. It emulates the live API's validation and
// server-side normalization (injected top-level defaults, filter expansion).
func decodeFlowBody(r *http.Request) (name, flowType, objectTypeID string, isEnabled bool, revisionID string, extra map[string]any, errMsg string) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return "", "", "", false, "", nil, "invalid JSON: " + err.Error()
	}
	name, _ = body["name"].(string)
	flowType, _ = body["type"].(string)
	objectTypeID, _ = body["objectTypeId"].(string)
	isEnabled, _ = body["isEnabled"].(bool)
	revisionID, _ = body["revisionId"].(string)
	if name == "" {
		return "", "", "", false, "", nil, "name is required"
	}
	switch flowType {
	case "CONTACT_FLOW":
		if objectTypeID == "" {
			objectTypeID = "0-1"
		}
	case "PLATFORM_FLOW":
		if objectTypeID == "" {
			return "", "", "", false, "", nil, "objectTypeId is required for PLATFORM_FLOW"
		}
	default:
		return "", "", "", false, "", nil, "type must be CONTACT_FLOW or PLATFORM_FLOW"
	}
	if _, ok := body["actions"]; !ok {
		return "", "", "", false, "", nil, "actions is required"
	}
	extra = map[string]any{}
	for k, v := range body {
		if slices.Contains(flowManagedKeys, k) {
			continue
		}
		extra[k] = v
	}
	normalizeFlowExtra(extra)
	return name, flowType, objectTypeID, isEnabled, revisionID, extra, ""
}

// normalizeFlowExtra emulates the beta API's server-side expansion of a flow:
// top-level defaults are injected when absent, every action gains an
// actionTypeVersion, and any embedded filter trees (enrollment criteria) get
// the same expansion the Lists API applies.
func normalizeFlowExtra(extra map[string]any) {
	if _, ok := extra["canEnrollFromSalesforce"]; !ok {
		extra["canEnrollFromSalesforce"] = false
	}
	if _, ok := extra["timeWindows"]; !ok {
		extra["timeWindows"] = []any{}
	}
	if _, ok := extra["blockedDates"]; !ok {
		extra["blockedDates"] = []any{}
	}
	if _, ok := extra["customProperties"]; !ok {
		extra["customProperties"] = []any{}
	}
	if _, ok := extra["crmObjectCreationStatus"]; !ok {
		extra["crmObjectCreationStatus"] = "COMPLETE"
	}
	if actions, ok := extra["actions"].([]any); ok {
		for _, a := range actions {
			if am, ok := a.(map[string]any); ok {
				if _, has := am["actionTypeVersion"]; !has {
					am["actionTypeVersion"] = float64(0)
				}
			}
		}
	}
	injectFilterDefaults(extra)
}

// flowResponse builds the JSON flow object as the live API returns it.
func flowResponse(fl *fakeFlow) map[string]any {
	out := map[string]any{
		"id":           fl.ID,
		"revisionId":   strconv.FormatInt(fl.RevisionID, 10),
		"name":         fl.Name,
		"type":         fl.Type,
		"objectTypeId": fl.ObjectTypeID,
		"isEnabled":    fl.IsEnabled,
		"flowType":     "WORKFLOW",
		"createdAt":    "2026-03-03T10:00:00.000Z",
		"updatedAt":    "2026-03-03T10:00:00.000Z",
	}
	for k, v := range fl.Extra {
		out[k] = v
	}
	return out
}

func (f *fakeHubSpot) createFlow(w http.ResponseWriter, r *http.Request) {
	name, flowType, objectTypeID, isEnabled, _, extra, errMsg := decodeFlowBody(r)
	if errMsg != "" {
		writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", errMsg)
		return
	}
	f.flowCounter++
	fl := &fakeFlow{
		ID:           fmt.Sprintf("%d", f.flowCounter),
		RevisionID:   1,
		Name:         name,
		Type:         flowType,
		ObjectTypeID: objectTypeID,
		IsEnabled:    isEnabled,
		Extra:        extra,
	}
	f.flows[fl.ID] = fl
	writeJSON(w, http.StatusCreated, flowResponse(fl))
}

// listFlows emulates GET /automation/v4/flows with cursor pagination. The
// page size is deliberately capped at 2 (below any real limit) so the
// data source's pagination loop is exercised by small test fixtures.
func (f *fakeHubSpot) listFlows(w http.ResponseWriter, r *http.Request) {
	ids := make([]string, 0, len(f.flows))
	for id := range f.flows {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, _ := strconv.Atoi(ids[i])
		b, _ := strconv.Atoi(ids[j])
		return a < b
	})

	start := 0
	if after := r.URL.Query().Get("after"); after != "" {
		n, err := strconv.Atoi(after)
		if err != nil {
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid after cursor")
			return
		}
		start = n
	}
	const pageSize = 2
	end := start + pageSize
	if end > len(ids) {
		end = len(ids)
	}
	results := make([]map[string]any, 0, pageSize)
	for _, id := range ids[start:end] {
		results = append(results, flowResponse(f.flows[id]))
	}
	out := map[string]any{"results": results}
	if end < len(ids) {
		out["paging"] = map[string]any{"next": map[string]any{"after": strconv.Itoa(end)}}
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *fakeHubSpot) flowByID(w http.ResponseWriter, r *http.Request, id string) {
	fl := f.flows[id]
	switch r.Method {
	case http.MethodGet:
		if fl == nil {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "flow not found")
			return
		}
		writeJSON(w, http.StatusOK, flowResponse(fl))
	case http.MethodPut:
		if fl == nil {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "flow not found")
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "unreadable body")
			return
		}
		f.lastFlowPut = raw
		r.Body = io.NopCloser(bytes.NewReader(raw))
		name, flowType, objectTypeID, isEnabled, revisionID, extra, errMsg := decodeFlowBody(r)
		if errMsg != "" {
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", errMsg)
			return
		}
		// Optimistic lock: the PUT must carry the current revisionId.
		if revisionID != strconv.FormatInt(fl.RevisionID, 10) {
			writeHubSpotError(w, http.StatusConflict, "CONFLICT",
				fmt.Sprintf("Flow revision id %s is not the latest revision id %d", revisionID, fl.RevisionID))
			return
		}
		if flowType != fl.Type {
			writeHubSpotError(w, http.StatusBadRequest, "VALIDATION_ERROR", "type cannot be changed")
			return
		}
		fl.Name = name
		fl.ObjectTypeID = objectTypeID
		fl.IsEnabled = isEnabled
		fl.Extra = extra
		fl.RevisionID++
		writeJSON(w, http.StatusOK, flowResponse(fl))
	case http.MethodDelete:
		if fl == nil {
			writeHubSpotError(w, http.StatusNotFound, "OBJECT_NOT_FOUND", "flow not found")
			return
		}
		delete(f.flows, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeHubSpotError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", r.Method)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeHubSpotError(w http.ResponseWriter, status int, category, message string) {
	writeJSON(w, status, map[string]string{
		"status":        "error",
		"message":       message,
		"category":      category,
		"correlationId": "fake-correlation-id",
	})
}
