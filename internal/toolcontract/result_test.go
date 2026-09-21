package toolcontract

import (
	"net/url"
	"strings"
	"testing"
)

func TestDecodeExternalizedResultUsesOneClosedContract(t *testing.T) {
	valid := `{"artifact_id":"artifact-1","content_type":"application/json","content_url":"/api/artifacts/artifact-1/versions/version-1","outcome":"succeeded","preview":"{","sha256":"` + strings.Repeat("a", 64) + `","size_bytes":8192,"truncated":true,"version_id":"version-1"}`
	for _, outcome := range []string{"succeeded", "failed", "unavailable", "partial"} {
		raw := strings.Replace(valid, `"succeeded"`, `"`+outcome+`"`, 1)
		descriptor, ref, found, err := DecodeExternalizedResult([]byte(raw))
		if err != nil || !found || ref != "artifact-version:version-1" || descriptor.ArtifactID != "artifact-1" || descriptor.Outcome != outcome {
			t.Fatalf("outcome=%s descriptor=%#v ref=%q found=%t err=%v", outcome, descriptor, ref, found, err)
		}
	}
	current := strings.TrimSuffix(valid, "}") + `,"read_with":"read_file(version_id=\"version-1\")"}`
	descriptor, ref, found, err := DecodeExternalizedResult([]byte(current))
	if err != nil || !found || ref != "artifact-version:version-1" ||
		descriptor.ReadWith != `read_file(version_id="version-1")` {
		t.Fatalf("current descriptor=%#v ref=%q found=%t err=%v", descriptor, ref, found, err)
	}
	if _, _, found, err := DecodeExternalizedResult([]byte(`{"artifact_id":"domain","value":1}`)); err != nil || found {
		t.Fatalf("ordinary payload found=%t err=%v", found, err)
	}
	for name, raw := range map[string]string{
		"unknown": strings.TrimSuffix(valid, "}") + `,"extra":true}`,
		"outcome": strings.Replace(valid, `"succeeded"`, `"unexpected"`, 1),
		"url":     strings.Replace(valid, `/api/artifacts/artifact-1/versions/version-1`, `/api/artifacts/artifact-1`, 1),
		"digest":  strings.Replace(valid, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"read":    strings.Replace(current, `version_id=\"version-1\"`, `version_id=\"another-version\"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, found, err := DecodeExternalizedResult([]byte(raw)); err == nil || !found {
				t.Fatalf("invalid descriptor found=%t err=%v", found, err)
			}
		})
	}
}

func TestCanonicalReadWithEscapesVersionIdentity(t *testing.T) {
	versionID := "version\"quoted\\path"
	readWith := CanonicalReadWith(versionID)
	if readWith != `read_file(version_id="version\"quoted\\path")` {
		t.Fatalf("canonical read hint=%q", readWith)
	}
	descriptor := ExternalizedResultDescriptor{
		ArtifactID: "artifact-1", VersionID: versionID, SHA256: strings.Repeat("a", 64),
		SizeBytes: 1, ContentType: "application/json", Outcome: "succeeded",
		ContentURL: "/api/artifacts/artifact-1/versions/" + url.PathEscape(versionID),
		ReadWith:   readWith, Preview: "{", Truncated: true,
	}
	if err := ValidateExternalizedResultDescriptor(descriptor); err != nil {
		t.Fatal(err)
	}
}
