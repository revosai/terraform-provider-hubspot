# HubSpot's Reporting API (public beta) cannot create a report from scratch
# or read/write its configuration (data sources, filters, visualization).
# Build a template report in the HubSpot UI once, then clone it as code: the
# clone copies the template's configuration, and Terraform manages the
# metadata (name, description, owner, business unit, permissions).
data "hubspot_report" "template" {
  name = "Template: Deals by stage"
}

resource "hubspot_report" "emea_pipeline" {
  source_report_id = data.hubspot_report.template.id # create-time only
  name             = "EMEA pipeline by stage"
  description      = "Open EMEA deals grouped by stage. Managed by Terraform."

  permissions = {
    type = "EVERYONE_VIEW"
  }
}

# Sharing with specific teams/users. A report carries a single specific
# permission level: set either `view` or `edit`, not both.
resource "hubspot_report" "sales_leadership" {
  source_report_id = "12345678"
  name             = "Sales leadership: quarterly bookings"
  owner_user_id    = "7654321"

  permissions = {
    type = "SPECIFIC"
    edit = [
      { type = "TEAM", id = "42" },
      { type = "USER", id = "7654321" },
    ]
  }
}

# A report built in the UI can also be adopted as-is with an import block
# (no source_report_id needed). Destroying a managed report ARCHIVES it in
# HubSpot — imported reports included. To stop managing a report without
# archiving it, delete its resource block and add (Terraform >= 1.7,
# OpenTofu >= 1.7):
#
# removed {
#   from = hubspot_report.emea_pipeline
#   lifecycle {
#     destroy = false
#   }
# }
