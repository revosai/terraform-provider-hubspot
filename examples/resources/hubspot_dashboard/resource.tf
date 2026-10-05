# A dashboard managed via the Analytics Reporting API (public beta). The API
# cannot create reports from scratch: build them in the HubSpot UI (or clone
# them with hubspot_report) and reference their IDs here.
resource "hubspot_dashboard" "sales_leadership" {
  name        = "Sales leadership"
  description = "Pipeline health and rep activity for the weekly forecast call."

  permissions = {
    type = "SPECIFIC"
    view = [{ type = "TEAM", id = "42" }]
    edit = [{ type = "USER", id = "7" }]
  }

  # Exact widget membership: reports not listed here are removed from the
  # dashboard. Omit report_ids entirely to leave the widgets of a dashboard
  # arranged in the UI untouched. Widget layout is read-only (`widgets`).
  report_ids = ["10101", "10102"]
}

# A copy of an existing dashboard (e.g. a UI-built template). The clone
# arguments only apply at create time; changing them later replaces the
# dashboard. With clone_reports = true HubSpot also copies every report, so
# the new dashboard's widgets point at NEW report IDs.
resource "hubspot_dashboard" "sales_leadership_emea" {
  name                    = "Sales leadership (EMEA)"
  clone_from_dashboard_id = hubspot_dashboard.sales_leadership.id
  clone_reports           = true
  owner_user_id           = "7"

  permissions = {
    type = "EVERYONE_VIEW"
  }
}
