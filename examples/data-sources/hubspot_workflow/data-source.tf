# Look up an existing workflow by its exact name (errors if the name is
# ambiguous) — handy for referencing unmanaged automation, or for backing up
# a flow definition before adopting it with `terraform import`.
data "hubspot_workflow" "welcome" {
  name = "Welcome new leads"
}

# ...or directly by flow ID.
data "hubspot_workflow" "by_id" {
  flow_id = "567890123"
}

# The complete flow definition as returned by the Automation v4 API, e.g. for
# writing point-in-time backups of automation alongside your state.
output "welcome_flow_backup" {
  value = data.hubspot_workflow.welcome.flow_json
}
