package sonarr

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/pandorest/client"
)

// service is what is Sonarr's own about being talked to, which the base
// client every generated method sends through is made from (see New): what it
// is called, how its refusals read, and what is known of them. This file is
// written by hand and lives beside the generated ones; everything else in the
// package is pandorest's.
var service = client.Service{
	Name: "Sonarr",
	// Sonarr writes the user agent in its log beside each request
	UserAgent:      "sonarr-mcp/" + version.Version,
	Example:        "nas:8989",
	RedirectAdvice: "set the server url to the address Sonarr itself answers on, with its URL base if it has one (Settings → General → URL Base)",
	WebPageAdvice:  "check the server url: it must be Sonarr's own address, with its URL base if it has one, not a proxy's login page",
	Messages:       errorMessages,
	Note:           note,
}

// note says what is known of a failure on a Sonarr.
func note(f client.Failure) string {
	if f.StatusCode == http.StatusUnauthorized {
		return "API key rejected; check the key under Settings → General → Security"
	}

	return ""
}

// validationFailure is one entry of the array Sonarr answers a failed
// validation with (FluentValidation's ValidationFailure).
type validationFailure struct {
	PropertyName string `json:"propertyName"`
	ErrorMessage string `json:"errorMessage"`
	// Severity is "error" or "warning"; a warning alone does not fail a
	// request, so one in an error answer rode along with a real failure.
	Severity string `json:"severity"`
}

// errorModel is the body of every other failure: a 404 for an id that does
// not exist, a 409 for a conflict, a 500 with the exception's message.
type errorModel struct {
	Message     string `json:"message"`
	Description string `json:"description"`
}

// errorMessages reads what Sonarr said went wrong out of an error body, or
// nothing when the body is not one of its shapes: each failed validation as
// "Field: message", or the message of any other error.
func errorMessages(body []byte) []string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil
	}

	var failures []validationFailure
	if body[0] == '[' && json.Unmarshal(body, &failures) == nil {
		var out []string
		for _, f := range failures {
			if f.ErrorMessage == "" {
				continue
			}
			m := f.ErrorMessage
			if f.PropertyName != "" {
				m = f.PropertyName + ": " + m
			}
			if strings.EqualFold(f.Severity, "warning") {
				m += " (warning)"
			}
			out = append(out, m)
		}

		return out
	}

	var em errorModel
	if body[0] == '{' && json.Unmarshal(body, &em) == nil && em.Message != "" {
		return []string{em.Message}
	}

	return nil
}
