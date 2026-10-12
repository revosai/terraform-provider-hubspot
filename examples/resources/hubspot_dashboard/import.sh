# Dashboards are imported using their numeric dashboard ID (visible in the
# dashboard URL in the HubSpot UI, or via the hubspot_dashboards data source).
# After import report_ids is null, so the widgets stay unmanaged; adding
# report_ids to the configuration adopts the dashboard's widget membership.
terraform import hubspot_dashboard.sales_leadership '24680135'

# OpenTofu uses the same ID:
tofu import hubspot_dashboard.sales_leadership '24680135'
