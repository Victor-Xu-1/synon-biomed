package config

import (
	"math"
	"testing"
	"time"

	"synon-go/internal/httpreliability"
)

func TestNetworkTimeoutBudgetsHaveSeparateMeanings(t *testing.T) {
	var network NetworkPolicyConfig
	if err := applyNetworkTimeouts(&network); err != nil {
		t.Fatal(err)
	}
	if network.ResponseHeaderTimeoutSeconds != int64(httpreliability.DefaultHeaderTimeout/time.Second) ||
		network.ReadIdleTimeoutSeconds != 30 || network.TransferIdleTimeoutSeconds != 300 || network.SearchTimeoutSeconds != 90 {
		t.Fatalf("defaults=%#v", network)
	}
	t.Setenv("SYNON_NETWORK_TRANSFER_IDLE_TIMEOUT_SECONDS", "1200")
	if err := applyNetworkTimeouts(&network); err != nil || network.TransferIdleTimeoutSeconds != 1200 || network.SearchTimeoutSeconds != 90 {
		t.Fatalf("budget coupling: %#v %v", network, err)
	}
}

func TestNetworkTimeoutBudgetRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"0", "-1", "not-a-duration", "9223372036854775807"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("SYNON_NETWORK_SEARCH_TIMEOUT_SECONDS", value)
			if err := applyNetworkTimeouts(&NetworkPolicyConfig{}); err == nil {
				t.Fatal("invalid environment budget accepted")
			}
		})
	}
	if err := applyNetworkTimeouts(&NetworkPolicyConfig{ReadIdleTimeoutSeconds: math.MaxInt64}); err == nil {
		t.Fatal("overflowing file configuration accepted")
	}
}
