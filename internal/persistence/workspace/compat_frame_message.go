package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ActivateCompletedCompatibilityFrameRequest performs the completed-to-processing
// compare-and-swap required by the public sendFrameMessage contract.
func (s *Store) ActivateCompletedCompatibilityFrameRequest(frameID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return false, errors.New("frame id is required")
	}
	activated, err := s.activateCompatibilityFrameRequest(
		context.Background(), frameID, compatibilityFrameActivationCompleted,
	)
	if err != nil {
		return false, fmt.Errorf("activate completed compatibility frame request: %w", err)
	}
	return activated, nil
}
