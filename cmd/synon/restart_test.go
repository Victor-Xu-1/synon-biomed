package main

import (
	"errors"
	"testing"
)

func TestRuntimeRestartRequesterQueuesOneReasonAndRejectsDuplicates(t *testing.T) {
	requests := make(chan string, 1)
	request := newRuntimeRestartRequester(requests)
	if err := request("data_dir_move"); err != nil {
		t.Fatal(err)
	}
	if err := request("duplicate"); err == nil {
		t.Fatal("duplicate restart request accepted")
	}
	if reason := <-requests; reason != "data_dir_move" {
		t.Fatalf("restart reason = %q", reason)
	}
	if err := request("after-consume"); err == nil {
		t.Fatal("restart request accepted after the first request was consumed")
	}
	if err := request(""); err == nil {
		t.Fatal("empty restart reason accepted")
	}
}

func TestRuntimeRestartRequestedErrorCarriesReason(t *testing.T) {
	err := &runtimeRestartRequestedError{Reason: "data_dir_change"}
	var target *runtimeRestartRequestedError
	if !errors.As(err, &target) || target.Reason != "data_dir_change" {
		t.Fatalf("restart error = %#v", err)
	}
}
