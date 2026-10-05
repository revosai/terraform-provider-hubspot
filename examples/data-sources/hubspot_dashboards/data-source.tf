# Snapshot every dashboard in the portal to git: one canonical JSON file per
# dashboard, keyed by ID. raw_json strips view-tracking fields, so a plan only
# shows a diff (and the repo only gets a commit) when a dashboard actually
# changed. Report configuration (query/visualization) is not exposed by the
# HubSpot API, so it cannot be part of the snapshot.
data "hubspot_dashboards" "all" {
  # Also capture widget layout and the reports on each dashboard.
  include_widgets = true
}

resource "local_file" "dashboard_snapshot" {
  for_each = { for d in data.hubspot_dashboards.all.dashboards : d.id => d }

  filename = "${path.module}/reporting/dashboards/${each.key}.json"
  content  = each.value.raw_json
}

# Filters combine with AND; within a set filter any value matches.
data "hubspot_dashboards" "sales_team" {
  query          = "sales" # name or description contains "sales"
  owner_user_ids = ["1234567", "7654321"]
}

output "sales_team_dashboard_ids" {
  value = data.hubspot_dashboards.sales_team.ids
}
