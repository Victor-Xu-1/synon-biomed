package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSlowLLMFixtureContract(t *testing.T) {
	delay := 20 * time.Millisecond
	server := httptest.NewServer(slowHandler(delay))
	defer server.Close()
	started := time.Now()
	response, err := http.Get(server.URL + "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if elapsed := time.Since(started); elapsed < delay {
		t.Fatalf("fixture returned after %s, before delay %s", elapsed, delay)
	}
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.StatusCode)
	}
}
