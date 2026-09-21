package toolcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

const MaxIdentityBytes = 512

type ExternalizedResultDescriptor struct {
	ArtifactID  string `json:"artifact_id"`
	VersionID   string `json:"version_id"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentType string `json:"content_type"`
	Outcome     string `json:"outcome"`
	ContentURL  string `json:"content_url"`
	ReadWith    string `json:"read_with,omitempty"`
	Preview     string `json:"preview"`
	Truncated   bool   `json:"truncated"`
}

func DecodeExternalizedResult(raw []byte) (ExternalizedResultDescriptor, string, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ExternalizedResultDescriptor{}, "", false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return ExternalizedResultDescriptor{}, "", false, err
	}
	for _, key := range []string{"artifact_id", "version_id", "content_url", "truncated"} {
		if _, found := fields[key]; !found {
			return ExternalizedResultDescriptor{}, "", false, nil
		}
	}
	if len(fields) != 9 && len(fields) != 10 {
		return ExternalizedResultDescriptor{}, "", true, errors.New("externalized tool result has an invalid shape")
	}
	if len(fields) == 10 {
		if _, found := fields["read_with"]; !found {
			return ExternalizedResultDescriptor{}, "", true, errors.New("externalized tool result has an invalid shape")
		}
	}
	var descriptor ExternalizedResultDescriptor
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil || decoder.Decode(&struct{}{}) == nil {
		return ExternalizedResultDescriptor{}, "", true, errors.New("externalized tool result is invalid")
	}
	if err := ValidateExternalizedResultDescriptor(descriptor); err != nil {
		return ExternalizedResultDescriptor{}, "", true, err
	}
	return descriptor, "artifact-version:" + descriptor.VersionID, true, nil
}

func ValidateExternalizedResultDescriptor(descriptor ExternalizedResultDescriptor) error {
	if descriptor.ArtifactID == "" || descriptor.ArtifactID != strings.TrimSpace(descriptor.ArtifactID) ||
		descriptor.VersionID == "" || descriptor.VersionID != strings.TrimSpace(descriptor.VersionID) ||
		len(descriptor.ArtifactID) > MaxIdentityBytes || len(descriptor.VersionID) > MaxIdentityBytes ||
		!ValidLowerHexSHA256(descriptor.SHA256) || descriptor.SizeBytes <= 0 ||
		descriptor.ContentType != "application/json" ||
		(descriptor.Outcome != "succeeded" && descriptor.Outcome != "failed" &&
			descriptor.Outcome != "unavailable" && descriptor.Outcome != "partial") ||
		descriptor.ContentURL != "/api/artifacts/"+url.PathEscape(descriptor.ArtifactID)+
			"/versions/"+url.PathEscape(descriptor.VersionID) ||
		(descriptor.ReadWith != "" && descriptor.ReadWith != CanonicalReadWith(descriptor.VersionID)) ||
		descriptor.Preview == "" || !utf8.ValidString(descriptor.Preview) || !descriptor.Truncated {
		return errors.New("externalized tool result is invalid")
	}
	return nil
}

// CanonicalReadWith is the single producer/validator encoding for the
// human-readable immutable-version hint. JSON string encoding keeps quotes,
// backslashes and control characters in an identity from changing the parser
// shape of the hint.
func CanonicalReadWith(versionID string) string {
	encoded, _ := json.Marshal(versionID)
	return `read_file(version_id=` + string(encoded) + `)`
}

func ValidLowerHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	return strings.IndexFunc(value, func(current rune) bool {
		return (current < '0' || current > '9') && (current < 'a' || current > 'f')
	}) < 0
}
