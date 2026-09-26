package config

import (
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"synon-go/internal/httpreliability"
)

// These budgets have different lifecycle meanings. In particular there is no
// full-transfer wall-clock budget or file-size ceiling in this configuration.
func applyNetworkTimeouts(network *NetworkPolicyConfig) error {
	for _, field := range []struct {
		name     string
		value    *int64
		fallback time.Duration
	}{
		{"RESPONSE_HEADER_TIMEOUT_SECONDS", &network.ResponseHeaderTimeoutSeconds, httpreliability.DefaultHeaderTimeout},
		{"READ_IDLE_TIMEOUT_SECONDS", &network.ReadIdleTimeoutSeconds, httpreliability.DefaultReadIdleTimeout},
		{"TRANSFER_IDLE_TIMEOUT_SECONDS", &network.TransferIdleTimeoutSeconds, httpreliability.DefaultTransferIdleTimeout},
		{"SEARCH_TIMEOUT_SECONDS", &network.SearchTimeoutSeconds, httpreliability.DefaultSearchTimeout},
	} {
		if raw, present := os.LookupEnv("SYNON_NETWORK_" + field.name); present {
			value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if err != nil || value <= 0 {
				return errors.New("SYNON_NETWORK_" + field.name + " must be positive seconds")
			}
			*field.value = value
		}
		if *field.value < 0 || *field.value > math.MaxInt64/int64(time.Second) {
			return errors.New("network " + strings.ToLower(field.name) + " is outside the duration range")
		}
		if *field.value == 0 {
			*field.value = int64(field.fallback / time.Second)
		}
	}
	return nil
}
