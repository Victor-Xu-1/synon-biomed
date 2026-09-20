package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeScientificArtifactValidationStrictContract(t *testing.T) {
	const canonical = `{"schemaVersion":2,"format":"smi","ok":true,"code":"valid_smiles","delimiterCount":1,"supplierRecordCount":1,"parsedCount":1,"invalidRecordCount":0,"atomCountRecords":1,"terminalDelimiter":true,"rdkitVersion":"2024.03.5"}`

	result, err := decodeScientificArtifactValidation(strings.NewReader(canonical), "smi", "2024.03.5")
	if err != nil {
		t.Fatalf("decode canonical validator result: %v", err)
	}
	if !result.OK || result.Code != "valid_smiles" || result.RDKitVersion != "2024.03.5" {
		t.Fatalf("canonical validator result = %#v", result)
	}

	wrongCase := strings.Replace(canonical, `"rdkitVersion"`, `"rdKitVersion"`, 1)
	if _, err := decodeScientificArtifactValidation(strings.NewReader(wrongCase), "smi", "2024.03.5"); err == nil {
		t.Fatal("strict validator contract accepted an unknown rdKitVersion field")
	}
	for _, field := range []string{"schemaVersion", "format", "ok", "code", "delimiterCount", "supplierRecordCount", "parsedCount", "invalidRecordCount", "atomCountRecords", "terminalDelimiter"} {
		var values map[string]any
		if err := json.Unmarshal([]byte(canonical), &values); err != nil {
			t.Fatal(err)
		}
		delete(values, field)
		raw, _ := json.Marshal(values)
		if _, err := decodeScientificArtifactValidation(strings.NewReader(string(raw)), "smi", "2024.03.5"); err == nil {
			t.Fatalf("missing %s accepted", field)
		}
	}
	for _, alteration := range [][2]string{{`"schemaVersion":2`, `"schemaVersion":1`}, {`"parsedCount":1`, `"parsedCount":2`}, {`"invalidRecordCount":0`, `"invalidRecordCount":-1`}, {`"atomCountRecords":1`, `"atomCountRecords":0`}, {`"rdkitVersion":"2024.03.5"`, `"rdkitVersion":"other"`}} {
		raw := strings.Replace(canonical, alteration[0], alteration[1], 1)
		if _, err := decodeScientificArtifactValidation(strings.NewReader(raw), "smi", "2024.03.5"); err == nil {
			t.Fatalf("invalid counter/protocol accepted: %s", raw)
		}
	}
}
