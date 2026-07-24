# Changelog

## 0.1.0 (July 24, 2026)

FEATURES:

* **New Provider:** `hubspot` — manage HubSpot portal configuration (config plane, not CRM records) as code
* **New Resource:** `hubspot_property_group` — manage CRM property groups (create/update/import; replace on `object_type`/`name` change)
* **New Resource:** `hubspot_property` — manage CRM object properties incl. enumeration options (list order = display order), archive-on-destroy with name-purgatory error handling, `{object_type}/{name}` import
* **New Resource:** `hubspot_pipeline` — manage deal/ticket/custom-object pipelines with inline stages (matched by `stage_id` so reorders/renames are in-place updates), string-typed deal-stage probabilities, `validateReferencesBeforeDelete` on destroy, `{object_type}/{pipeline_id}` import
* **New Resource:** `hubspot_object_schema` — manage custom object definitions (Enterprise): labels, display/required/searchable properties, create-time bootstrap properties and associations, `name` RequiresReplace, `force_delete`-gated two-phase (archive-then-purge) destroy, import by `object_type_id`
* **New Resource:** `hubspot_association_label` — manage custom association labels (Associations v4), paired (`inverse_label`) or unpaired; `name` is write-only/RequiresReplace (not returned by HubSpot), paired↔unpaired flips RequiresReplace, `{from_object_type}/{to_object_type}/{type_id}` import
* **New Resource:** `hubspot_list` — manage `MANUAL`/`DYNAMIC`/`SNAPSHOT` CRM lists (Lists v3), definition only (membership never tracked); `filter_branch` JSON is compared semantically to absorb server-injected defaults; SNAPSHOT filter edits RequiresReplace; archive-on-destroy (restorable 90 days), import by list id
* **New Data Source:** `hubspot_property` — read any property (including HubSpot-defined defaults) by object type and name
* **New Data Source:** `hubspot_properties` — list every property on an object type (archived excluded unless `include_archived`), for HCL-side filtering (e.g. custom-only)
* **New Data Source:** `hubspot_owner` — look up a CRM owner by email or owner ID
* **New Data Source:** `hubspot_portal` — the authenticated portal's ID and account defaults
* **New Data Source:** `hubspot_pipeline` — read a pipeline and its stages by `object_type` and `pipeline_id`
* **New Data Source:** `hubspot_object_schema` — resolve a custom object's portal-specific `object_type_id` by name
* **New Data Source:** `hubspot_association_labels` — list association labels between an object-type pair (resolve portal-specific `type_id`s by name)

NOTES:

* Documentation & repository hygiene: registry guides (authentication/scopes, getting-started, destroy semantics), import examples for all resources, README badges, and community-health files (`CODE_OF_CONDUCT.md`, `SECURITY.md`, `SUPPORT.md`, `RELEASING.md`, issue/PR templates, `CODEOWNERS`, Dependabot). CI now validates and spell-checks generated docs.
