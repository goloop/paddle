package paddle

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrNotFound matches an *APIError with status 404, and is returned by
// CustomerByEmail when no customer has the address.
var ErrNotFound = errors.New("paddle: not found")

// APIError is an error response from the API.
type APIError struct {
	// Status is the HTTP status code.
	Status int

	// Type is "request_error" or "api_error"; Code is the machine-readable
	// reason ("not_found", "invalid_field", "customer_already_exists", ...);
	// Detail is the human explanation.
	Type   string
	Code   string
	Detail string

	// DocumentationURL explains the code.
	DocumentationURL string

	// Errors lists the fields that failed validation, on a 400.
	Errors []FieldError

	// RequestID identifies the request to Paddle support.
	RequestID string

	// Raw is the response body as received.
	Raw json.RawMessage
}

// FieldError is one field that failed validation.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("paddle: %d", e.Status)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	for _, f := range e.Errors {
		msg += fmt.Sprintf("; %s: %s", f.Field, f.Message)
	}
	return msg
}

// Is makes errors.Is(err, ErrNotFound) true for a 404.
func (e *APIError) Is(target error) bool {
	return target == ErrNotFound && e.Status == 404
}

func parseError(status int, body []byte) error {
	var w struct {
		Error struct {
			Type             string       `json:"type"`
			Code             string       `json:"code"`
			Detail           string       `json:"detail"`
			DocumentationURL string       `json:"documentation_url"`
			Errors           []FieldError `json:"errors"`
		} `json:"error"`
		Meta struct {
			RequestID string `json:"request_id"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(body, &w)
	return &APIError{
		Status:           status,
		Type:             w.Error.Type,
		Code:             w.Error.Code,
		Detail:           w.Error.Detail,
		DocumentationURL: w.Error.DocumentationURL,
		Errors:           w.Error.Errors,
		RequestID:        w.Meta.RequestID,
		Raw:              append(json.RawMessage(nil), body...),
	}
}
