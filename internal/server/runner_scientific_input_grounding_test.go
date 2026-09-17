package server

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"synon-go/internal/sciencecapability"
)

func TestManagedExecutionPackInputReceiptAcceptsOptionalEvidenceInputs(t *testing.T) {
	receptor := strings.Repeat("a", 64)
	ligand := strings.Repeat("b", 64)
	pocket := strings.Repeat("c", 64)
	inputs := []sciencecapability.ExecutionInput{
		{Kind: "receptor"},
		{Kind: "ligand"},
	}
	stdout := fmt.Sprintf(
		`SYNON_EXECUTION_PACK_INPUT_RECEIPT={"schema":"synon.execution-pack-input-receipt.v1","execution_pack_id":"molecular-docking.engine","inputs":{"receptor":%q,"ligand":%q,"pocket_selection":%q,"pocket_validation":null}}`,
		receptor, ligand, pocket,
	)
	got, valid := managedExecutionPackInputDigestsFromStdout(stdout, "molecular-docking.engine", inputs)
	want := []string{receptor, ligand, pocket}
	if !valid || !reflect.DeepEqual(got, want) {
		t.Fatalf("optional evidence receipt digests=%v valid=%t, want %v", got, valid, want)
	}

	badOptional := strings.Replace(stdout, pocket, "not-a-digest", 1)
	if _, valid := managedExecutionPackInputDigestsFromStdout(badOptional, "molecular-docking.engine", inputs); valid {
		t.Fatal("populated optional evidence without a digest was accepted")
	}
	missingRequired := strings.Replace(stdout, `"ligand":"`+ligand+`",`, "", 1)
	if _, valid := managedExecutionPackInputDigestsFromStdout(missingRequired, "molecular-docking.engine", inputs); valid {
		t.Fatal("receipt without a registered required input was accepted")
	}
}
