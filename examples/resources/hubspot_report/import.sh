# Reports are imported using their numeric report ID (the number in the
# report's URL in the HubSpot UI, or via the hubspot_reports data source).
# source_report_id stays unset after import, and adding it later never
# forces a replacement. The report's configuration stays UI-managed.
terraform import hubspot_report.emea_pipeline '12345678'

# OpenTofu uses the same ID:
tofu import hubspot_report.emea_pipeline '12345678'
