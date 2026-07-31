# A contact-based workflow managed as code via the Automation v4 API (beta).
# The flow graph (actions, enrollment criteria, connections) is passed through
# as JSON: HubSpot expands it with server-injected defaults on read-back, but
# the provider compares it semantically so an unchanged config plans empty.
# Updates are full-replace PUTs guarded by HubSpot's revisionId optimistic
# lock, which the provider handles automatically (GET-then-PUT).
resource "hubspot_workflow" "welcome_new_leads" {
  name      = "Welcome new leads"
  flow_type = "CONTACT_FLOW"

  # Keep new automation off until it has been reviewed in the portal UI.
  enabled = false

  flow_json = jsonencode({
    startActionId = "1"
    actions = [
      {
        actionId          = "1"
        type              = "SINGLE_CONNECTION"
        actionTypeVersion = 0
        actionTypeId      = "0-1" # delay
        fields = {
          delta     = "5"
          time_unit = "MINUTES"
        }
        connection = {
          edgeType     = "STANDARD"
          nextActionId = "2"
        }
      },
      {
        actionId          = "2"
        type              = "SINGLE_CONNECTION"
        actionTypeVersion = 0
        actionTypeId      = "0-5" # set property value
        fields = {
          property_name = "lifecyclestage"
          value = {
            staticValue = "lead"
            type        = "STATIC_VALUE"
          }
        }
      },
    ]
    enrollmentCriteria = {
      shouldReEnroll = false
      type           = "LIST_BASED"
      listFilterBranch = {
        filterBranchType     = "OR"
        filterBranchOperator = "OR"
        filterBranches = [
          {
            filterBranchType     = "AND"
            filterBranchOperator = "AND"
            filters = [
              {
                filterType = "PROPERTY"
                property   = "email"
                operation = {
                  operationType                = "ALL_PROPERTY"
                  operator                     = "IS_KNOWN"
                  includeObjectsWithNoValueSet = false
                }
              },
            ]
          },
        ]
      }
    }
  })
}

# A workflow on another CRM object (deals, tickets, custom objects) uses
# PLATFORM_FLOW and must name the object type it runs on.
resource "hubspot_workflow" "stale_deal_nudge" {
  name           = "Nudge stale deals"
  flow_type      = "PLATFORM_FLOW"
  object_type_id = "0-3" # deals

  flow_json = jsonencode({
    actions = []
  })
}
