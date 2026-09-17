package providers

import (
	"net/http"
	"testing"
)

func TestProviderOutputTokenMaximumUsesStructuredProviderConstraint(t *testing.T) {
	body := []byte(`{"error":{"message":"The parameter max_tokens is invalid: expected a value <= 131072, but got 256000.","param":"max_tokens"}}`)
	err := newResponseHTTPError(http.StatusBadRequest, "redacted provider error", body)
	maximum, found := ProviderOutputTokenMaximum(err)
	if !found || maximum != 131072 {
		t.Fatalf("maximum=%d found=%t err=%v", maximum, found, err)
	}
	if status, found := HTTPStatus(err); !found || status != http.StatusBadRequest {
		t.Fatalf("status=%d found=%t", status, found)
	}
}

func TestProviderOutputTokenMaximumDoesNotReuseUnrelatedParameterLimit(t *testing.T) {
	body := []byte(`{"error":{"message":"The parameter top_p is invalid: expected a value <= 1, but got 2.","param":"top_p"}}`)
	err := newResponseHTTPError(http.StatusBadRequest, "redacted provider error", body)
	if maximum, found := ProviderOutputTokenMaximum(err); found || maximum != 0 {
		t.Fatalf("unrelated constraint became output maximum: maximum=%d found=%t", maximum, found)
	}
}
