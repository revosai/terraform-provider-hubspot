# Look up a dashboard by its exact (case-sensitive) name. Errors when no
# dashboard, or more than one, has that name.
data "hubspot_dashboard" "sales" {
  name = "Sales overview"
}

# ...or by ID, including an archived dashboard.
data "hubspot_dashboard" "retired" {
  id       = "123456"
  archived = true
}

# The reports shown on the dashboard (sorted, distinct) and who can see it.
output "sales_dashboard_report_ids" {
  value = data.hubspot_dashboard.sales.report_ids
}

output "sales_dashboard_permission_type" {
  value = data.hubspot_dashboard.sales.permissions.type
}

# A canonical JSON snapshot (view-tracking fields stripped) for committing to
# git: it only changes when the dashboard really changes.
resource "local_file" "sales_dashboard_snapshot" {
  filename = "${path.module}/reporting/dashboards/${data.hubspot_dashboard.sales.id}.json"
  content  = data.hubspot_dashboard.sales.raw_json
}
