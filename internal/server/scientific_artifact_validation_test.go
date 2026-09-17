package server

import (
	"strings"
	"testing"
)

func TestDecodeScientificArtifactValidationStrictContract(t *testing.T) {
	const canonical = `{"schemaVersion":1,"format":"smi","ok":true,"code":"valid_smiles","delimiterCount":1,"supplierRecordCount":1,"parsedCount":1,"invalidRecordIndexes":[],"atomCounts":[3],"terminalDelimiter":true,"rdkitVersion":"2024.03.5"}`

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
}
