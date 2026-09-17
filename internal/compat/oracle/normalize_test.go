package oracle

import (
	"errors"
	"strings"
	"testing"
)

func TestCompareJSONNormalizesDeclaredPointersAndMapOrder(t *testing.T) {
	baseline := []byte("{\"request\":{\"id\":\"v1-id\",\"name\":\"same\"},\"response\":{\"body\":{\"b\":2,\"a\":1},\"status\":200},\"events\":[{\"timestamp\":\"old\",\"type\":\"done\"}]}")
	candidate := []byte("{\"events\":[{\"type\":\"done\",\"timestamp\":\"new\"}],\"response\":{\"status\":200,\"body\":{\"a\":1,\"b\":2}},\"request\":{\"name\":\"same\",\"id\":\"go-id\"}}")

	if err := CompareJSON(baseline, candidate, []string{"/request/id", "/events/0/timestamp"}); err != nil {
		t.Fatalf("CompareJSON() error = %v", err)
	}
}

func TestCompareJSONReportsPreciseBehaviorDifference(t *testing.T) {
	baseline := []byte("{\"response\":{\"status\":200,\"body\":{\"ok\":true}},\"events\":[\"created\",\"ready\"]}")
	candidate := []byte("{\"response\":{\"status\":500,\"body\":{\"ok\":true}},\"events\":[\"created\",\"ready\"]}")

	err := CompareJSON(baseline, candidate, nil)
	if !errors.Is(err, ErrBehaviorMismatch) || !strings.Contains(err.Error(), "/response/status") {
		t.Fatalf("CompareJSON() error = %v, want status path", err)
	}

	candidate = []byte("{\"response\":{\"status\":200,\"body\":{\"ok\":true}},\"events\":[\"ready\",\"created\"]}")
	err = CompareJSON(baseline, candidate, nil)
	if !errors.Is(err, ErrBehaviorMismatch) || !strings.Contains(err.Error(), "/events/0") {
		t.Fatalf("CompareJSON(array order) error = %v, want ordered event difference", err)
	}
}

func TestCompareJSONRejectsInvalidJSONAndNormalizationPointers(t *testing.T) {
	if err := CompareJSON([]byte("{"), []byte("{}"), nil); !errors.Is(err, ErrInvalidCapture) {
		t.Fatalf("CompareJSON(invalid JSON) error = %v", err)
	}
	if err := CompareJSON([]byte("{\"id\":1}"), []byte("{\"id\":2}"), []string{"id"}); !errors.Is(err, ErrInvalidCapture) {
		t.Fatalf("CompareJSON(invalid pointer) error = %v", err)
	}
	if err := CompareJSON([]byte("{\"id\":1}"), []byte("{\"other\":1}"), []string{"/id"}); !errors.Is(err, ErrInvalidCapture) {
		t.Fatalf("CompareJSON(missing pointer) error = %v", err)
	}
}

func TestRedactJSONRemovesSensitiveValuesBeforePersistence(t *testing.T) {
	capture := []byte("{\"response\":{\"token\":\"secret-token\",\"nested\":{\"password\":\"secret-password\"}}}")
	redacted, err := RedactJSON(capture, []string{"/response/token", "/response/nested/password"})
	if err != nil {
		t.Fatalf("RedactJSON() error = %v", err)
	}
	if strings.Contains(string(redacted), "secret-token") || strings.Contains(string(redacted), "secret-password") {
		t.Fatalf("RedactJSON() leaked secret: %s", redacted)
	}
	if strings.Count(string(redacted), "<redacted>") != 2 {
		t.Fatalf("RedactJSON() output = %s", redacted)
	}
}

func TestCompareJSONCandidateSupersetAllowsOnlyAdditionalCandidateObjectFields(t *testing.T) {
	baseline := []byte("{\"status\":\"healthy\",\"nested\":{\"required\":true}}")
	candidate := []byte("{\"status\":\"healthy\",\"name\":\"synon-go\",\"nested\":{\"required\":true,\"extra\":1}}")
	if err := CompareJSONMode(baseline, candidate, nil, CandidateSuperset); err != nil {
		t.Fatalf("CompareJSONMode(candidate-superset) error = %v", err)
	}
	if err := CompareJSON(baseline, candidate, nil); !errors.Is(err, ErrBehaviorMismatch) {
		t.Fatalf("CompareJSON(exact) error = %v, want mismatch", err)
	}
	missing := []byte("{\"status\":\"healthy\",\"nested\":{\"extra\":1}}")
	if err := CompareJSONMode(baseline, missing, nil, CandidateSuperset); !errors.Is(err, ErrBehaviorMismatch) || !strings.Contains(err.Error(), "/nested/required") {
		t.Fatalf("CompareJSONMode(missing required) error = %v", err)
	}
}

func TestCompareJSONCandidateSupersetMatchesUniqueNamedObjectArrays(t *testing.T) {
	baseline := []byte(`{"skills":[{"name":"alpha","value":1},{"name":"gamma","value":3}]}`)
	candidate := []byte(`{"skills":[{"name":"alpha","value":1},{"name":"beta","value":2},{"name":"gamma","value":3}]}`)
	if err := CompareJSONMode(baseline, candidate, nil, CandidateSuperset); err != nil {
		t.Fatalf("named candidate superset error = %v", err)
	}
	if err := CompareJSON(baseline, candidate, nil); !errors.Is(err, ErrBehaviorMismatch) {
		t.Fatalf("exact named array error = %v, want mismatch", err)
	}
	changed := []byte(`{"skills":[{"name":"alpha","value":9},{"name":"gamma","value":3}]}`)
	if err := CompareJSONMode(baseline, changed, nil, CandidateSuperset); !errors.Is(err, ErrBehaviorMismatch) || !strings.Contains(err.Error(), "/skills/alpha/value") {
		t.Fatalf("changed named item error = %v", err)
	}
	scalarBaseline := []byte(`{"events":["created","ready"]}`)
	scalarCandidate := []byte(`{"events":["created","extra","ready"]}`)
	if err := CompareJSONMode(scalarBaseline, scalarCandidate, nil, CandidateSuperset); !errors.Is(err, ErrBehaviorMismatch) {
		t.Fatalf("scalar array unexpectedly accepted: %v", err)
	}
}
