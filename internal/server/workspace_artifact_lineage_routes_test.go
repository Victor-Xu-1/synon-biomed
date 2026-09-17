package server

import (
	"net/http"
	"testing"
)

func TestArtifactReadCompatibilityDispatchersPreserveDownloadPaths(t *testing.T) {
	fixture := newArtifactReadFixture(t)

	current := artifactCompatibilityRequest(t, fixture.handler, "owner-a", "/api/artifacts/"+fixture.artifactID)
	if current.Code != http.StatusOK || current.Body.String() != "second" {
		t.Fatalf("current artifact download = %d: %q", current.Code, current.Body.String())
	}
	version := artifactCompatibilityRequest(t, fixture.handler, "owner-a", "/api/artifacts/versions/"+fixture.firstVersionID)
	if version.Code != http.StatusOK || version.Body.String() != "first" {
		t.Fatalf("artifact version download = %d: %q", version.Code, version.Body.String())
	}
}
