package server

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// validateAgentSavedArtifactTemplates enforces the final-bytes contract at the
// publication boundary. Completion review keeps the same check as defense in
// depth, but a model receives an actionable save_artifacts failure before any
// invalid version becomes canonical.
func validateAgentSavedArtifactTemplates(relativePath string, snapshot *os.File) error {
	if snapshot == nil {
		return nil
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(snapshot, maxRunnerCrossArtifactScanBytes+1))
	if err != nil {
		return err
	}
	if _, seekErr := snapshot.Seek(0, io.SeekStart); seekErr != nil {
		return seekErr
	}
	if len(data) > maxRunnerCrossArtifactScanBytes {
		return nil
	}
	failures := runnerCrossArtifactTemplateFailures(relativePath, string(data))
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", errAgentSavedArtifactTemplateUnresolved, strings.Join(failures, ", "))
}
