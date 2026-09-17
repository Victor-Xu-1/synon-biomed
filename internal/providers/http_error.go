package providers

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

type responseHTTPError struct {
	status          int
	message         string
	maxOutputTokens int
}

func (err *responseHTTPError) Error() string { return err.message }

// HTTPStatus uses transport evidence, never matching the text of a model or
// tool error. Callers can distinguish rejected requests from token stops.
func HTTPStatus(err error) (int, bool) {
	var response *responseHTTPError
	if !errors.As(err, &response) {
		return 0, false
	}
	return response.status, true
}

var providerExpectedMaximumPattern = regexp.MustCompile(`(?i)expected\s+(?:a\s+)?value\s*(?:<=|less\s+than\s+or\s+equal\s+to)\s*([0-9][0-9,]*)`)

func newResponseHTTPError(status int, message string, body []byte) *responseHTTPError {
	return &responseHTTPError{
		status: status, message: message, maxOutputTokens: providerOutputTokenMaximumFromBody(body),
	}
}

// ProviderOutputTokenMaximum returns only a provider-declared request
// constraint parsed from a structured HTTP error. It never infers a model
// maximum from generated usage or a user/model message.
func ProviderOutputTokenMaximum(err error) (int, bool) {
	var response *responseHTTPError
	if !errors.As(err, &response) || response.maxOutputTokens <= 0 {
		return 0, false
	}
	return response.maxOutputTokens, true
}

func providerOutputTokenMaximumFromBody(body []byte) int {
	type providerError struct {
		Param   string `json:"param"`
		Message string `json:"message"`
	}
	var envelope struct {
		Error providerError `json:"error"`
	}
	message := ""
	parameter := ""
	if json.Unmarshal(body, &envelope) == nil {
		message = strings.TrimSpace(envelope.Error.Message)
		parameter = strings.ToLower(strings.TrimSpace(envelope.Error.Param))
	}
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	lowerMessage := strings.ToLower(message)
	if parameter != "max_tokens" && parameter != "max_output_tokens" &&
		!strings.Contains(lowerMessage, "max_tokens") && !strings.Contains(lowerMessage, "max output tokens") {
		return 0
	}
	match := providerExpectedMaximumPattern.FindStringSubmatch(message)
	if len(match) != 2 {
		return 0
	}
	value, err := strconv.Atoi(strings.ReplaceAll(match[1], ",", ""))
	if err != nil || value <= 0 || value > 10_000_000 {
		return 0
	}
	return value
}
