package client

import (
	"errors"
	"fmt"
	"net/http"
)

// APIError represents a structured error response from the HubSpot API.
// HubSpot returns error bodies of the shape
// {"status","message","category","correlationId","policyName"}; when the body
// is not valid JSON, Message contains a snippet of the raw response instead.
type APIError struct {
	// StatusCode is the HTTP status code of the response.
	StatusCode int
	// Category is HubSpot's machine-readable error category,
	// e.g. "OBJECT_NOT_FOUND" or "VALIDATION_ERROR".
	Category string
	// Message is the human-readable error message.
	Message string
	// CorrelationID identifies the request in HubSpot's logs and should be
	// included when contacting HubSpot support.
	CorrelationID string
	// PolicyName identifies which rate-limit policy was exceeded on 429
	// responses, e.g. "TEN_SECONDLY_ROLLING" or "DAILY".
	PolicyName string
}

// Error implements the error interface.
func (e *APIError) Error() string {
	msg := fmt.Sprintf("hubspot: HTTP %d", e.StatusCode)
	if e.Category != "" {
		msg += fmt.Sprintf(" %s", e.Category)
	}
	if e.PolicyName != "" {
		msg += fmt.Sprintf(" (policy %s)", e.PolicyName)
	}
	if e.Message != "" {
		msg += fmt.Sprintf(": %s", e.Message)
	}
	if e.CorrelationID != "" {
		msg += fmt.Sprintf(" (correlationId %s)", e.CorrelationID)
	}
	return msg
}

// IsNotFound reports whether err is an *APIError with HTTP status 404.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// IsConflict reports whether err is an *APIError with HTTP status 409, e.g.
// an Automation v4 PUT rejected by the flow's revisionId optimistic lock.
func IsConflict(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict
}

// AsAPIError is an errors.As convenience wrapper for *APIError. It reports
// whether err (or any error it wraps) is an *APIError, storing it in *target.
func AsAPIError(err error, target **APIError) bool {
	return errors.As(err, target)
}
