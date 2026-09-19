package server

import (
	"net/url"
	"strings"
)

// agentArtifactURLs is the single URL projection for an immutable artifact
// version. Published results may also expose artifact_ref as the user-facing
// identity; these URLs are explicit machine-readable preview/content access.
func agentArtifactURLs(artifactID, versionID string) map[string]any {
	artifactID = strings.TrimSpace(artifactID)
	versionID = strings.TrimSpace(versionID)
	return map[string]any{
		"preview_url": "/#/artifacts/" + url.PathEscape(artifactID) + "?version=" + url.QueryEscape(versionID),
		"content_url": "/api/artifacts/" + url.PathEscape(artifactID) + "/versions/" + url.PathEscape(versionID),
	}
}

func agentArtifactReference(versionID string) string {
	return "{{artifact:" + strings.TrimSpace(versionID) + "}}"
}
