package providers

import (
	"context"
	"errors"
	"testing"
)

func TestResolveRunnerModelProfileHonorsCanceledResolutionContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ResolveRunnerModelProfile(nil, nil, nil, ResolutionInput{Context: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("resolution error = %v, want context.Canceled", err)
	}
}
