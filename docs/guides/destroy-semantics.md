---
page_title: "Destroy and archive behavior"
subcategory: ""
description: |-
  What terraform destroy does for each resource — HubSpot usually archives rather than deletes.
---

# Destroy and archive behavior

`terraform destroy` (and removing a resource from configuration) does whatever
the HubSpot API does — which is often **archive**, not hard delete. The
provider never fakes a successful delete of something HubSpot won't remove; it
returns an actionable error instead.

## Per-resource behavior

| Resource | On destroy | Notes |
|---|---|---|
| `hubspot_property_group` | **Deletes** the group | Fails if the group still contains properties. |
| `hubspot_property` | **Archives** the property | The internal name is reserved for ~90 days ("name purgatory"); recreating it too soon returns an actionable error. |
| `hubspot_pipeline` | **Deletes** the pipeline | Guarded against orphaning records (`validateReferencesBeforeDelete=true`). HubSpot's built-in **default** pipeline cannot be deleted — adopt it via `terraform import`; destroy returns guidance to `terraform state rm`. |
| `hubspot_object_schema` | **Deletes** the object type and **all its records** | Destructive — gated behind `force_delete = true`; performed as a two-phase archive-then-purge. |
| `hubspot_association_label` | **Deletes** the label | Removes the label from **every record association** that uses it — a potentially wide blast radius. HubSpot-defined labels cannot be deleted. |
| `hubspot_list` | **Archives** the list | Restorable within 90 days. Only the list definition is affected; records are never deleted. |
| `hubspot_workflow` | **Deletes** the workflow (moves it to HubSpot's deleted state) | Enrolled objects are unenrolled. Restorable in the HubSpot UI within 90 days. |

## Common patterns

- **Stop managing without deleting.** `terraform state rm <address>` drops the
  object from state and leaves it untouched in HubSpot. Use this for
  HubSpot-defined defaults you imported but no longer want to manage.
- **Name purgatory.** Because archived property (and object-schema) names stay
  reserved for ~90 days, destroying and immediately recreating with the same
  name fails. Either wait out the window or choose a new name.
- **Data-destroying replaces.** Changing an immutable attribute plans a
  `RequiresReplace`. Where the replacement destroys data (e.g. an object
  schema), the resource documentation calls it out — review the plan before
  applying.
