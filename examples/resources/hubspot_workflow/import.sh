# Workflows are imported using their flow ID (visible in the workflow URL in
# the HubSpot UI, or via the Automation v4 API).
# The configured flow_json is reconciled semantically on the next plan, so a
# textual difference from HubSpot's normalized form does not force a change.
terraform import hubspot_workflow.welcome_new_leads '567890123'

# OpenTofu uses the same ID:
tofu import hubspot_workflow.welcome_new_leads '567890123'
