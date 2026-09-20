package server

import (
	"context"
	"strings"
)

func runnerCrossArtifactTemplateFailures(name, content string) []string {
	failures, err := scanRunnerTemplateFailures(context.Background(), strings.NewReader(content), name)
	if err != nil {
		// A strings.Reader cannot fail. Keep an explicit failure if that
		// invariant changes rather than silently treating it as verification.
		return []string{"template_validation_unavailable:" + name}
	}
	return failures
}
