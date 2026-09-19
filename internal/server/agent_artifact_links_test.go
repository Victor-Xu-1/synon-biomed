package server

import "testing"

func TestAgentArtifactURLsBindPreviewAndContentToTheExactVersion(t *testing.T) {
	got := agentArtifactURLs("artifact/with space", "version/with space")
	if got["preview_url"] != "/#/artifacts/artifact%2Fwith%20space?version=version%2Fwith+space" {
		t.Fatalf("preview_url=%q", got["preview_url"])
	}
	if got["content_url"] != "/api/artifacts/artifact%2Fwith%20space/versions/version%2Fwith%20space" {
		t.Fatalf("content_url=%q", got["content_url"])
	}
}

func TestAgentArtifactReferenceUsesImmutableVersionIdentity(t *testing.T) {
	if got := agentArtifactReference(" version-1 "); got != "{{artifact:version-1}}" {
		t.Fatalf("artifact reference=%q", got)
	}
}

func assertAgentArtifactResultLinks(t *testing.T, artifact map[string]any) {
	t.Helper()
	artifactID := stringValue(artifact["artifact_id"])
	versionID := stringValue(artifact["version_id"])
	if artifact["uri"] != nil {
		t.Fatalf("obsolete artifact uri=%#v", artifact["uri"])
	}
	if artifact["preview_url"] != "/#/artifacts/"+artifactID+"?version="+versionID {
		t.Fatalf("preview_url=%#v", artifact["preview_url"])
	}
	if artifact["content_url"] != "/api/artifacts/"+artifactID+"/versions/"+versionID {
		t.Fatalf("content_url=%#v", artifact["content_url"])
	}
}
