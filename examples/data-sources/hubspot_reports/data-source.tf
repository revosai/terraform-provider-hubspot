# All reports on a given dashboard.
data "hubspot_dashboard" "sales" {
  name = "Sales overview"
}

data "hubspot_reports" "on_sales_dashboard" {
  dashboard_id = data.hubspot_dashboard.sales.id
}

# Orphaned reports (on no dashboard): candidates for clean-up.
data "hubspot_reports" "orphaned" {
  on_dashboard = false
}

output "orphaned_report_names" {
  value = [for r in data.hubspot_reports.orphaned.reports : r.name]
}

# Snapshot every report's metadata (permissions, tags, owner, ...) to git,
# one file per report keyed by ID.
data "hubspot_reports" "all" {}

resource "local_file" "report_snapshot" {
  for_each = { for r in data.hubspot_reports.all.reports : r.id => r }

  filename = "${path.module}/reporting/reports/${each.key}.json"
  content  = each.value.raw_json
}
