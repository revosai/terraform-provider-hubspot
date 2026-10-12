# Look up a report by its exact (case-sensitive) name. Errors when no report,
# or more than one, has that name.
data "hubspot_report" "revenue" {
  name = "Revenue by rep"
}

# ...or by ID, including an archived report.
data "hubspot_report" "legacy" {
  id       = "987654"
  archived = true
}

# Report IDs are what dashboards reference and what hubspot_report clones from.
output "revenue_report_id" {
  value = data.hubspot_report.revenue.id
}

# Metadata snapshot (permissions, tags, owner, ...) for committing to git.
# Report configuration (query/visualization) is not exposed by the HubSpot API.
resource "local_file" "revenue_report_snapshot" {
  filename = "${path.module}/reporting/reports/${data.hubspot_report.revenue.id}.json"
  content  = data.hubspot_report.revenue.raw_json
}
