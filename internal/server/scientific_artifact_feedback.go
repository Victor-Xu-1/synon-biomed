package server

import (
	"errors"
	"fmt"
)

func scientificArtifactValidatorContractError() error {
	return fmt.Errorf("%w: validator_contract_mismatch", errScientificArtifactValidationUnavailable)
}

// Only the managed validator's defined data outcomes can blame artifact bytes.
// Process/protocol failures must remain runtime failures, not repair-file advice.
func scientificArtifactDataFailureCode(format, code string) bool {
	switch format {
	case "sdf":
		switch code {
		case "empty_sdf", "missing_record_delimiter", "invalid_sdf_records", "missing_terminal_delimiter", "record_count_mismatch":
			return true
		}
	case "smi":
		switch code {
		case "invalid_utf8", "empty_smiles_records", "empty_smiles", "invalid_smiles_records":
			return true
		}
	}
	return false
}

func addScientificArtifactFailureFeedback(failure map[string]any, err error) {
	var invalid *scientificArtifactValidationError
	if !errors.As(err, &invalid) {
		return
	}
	result := invalid.Validation
	if !scientificArtifactDataFailureCode(result.Format, invalid.Code) {
		return
	}
	// Do not expose parser stderr, arbitrary error strings, paths or raw molecules.
	// Counts summarize observed records; they do not identify individual bad lines.
	failure["validation_code"] = invalid.Code
	failure["validation_format"] = result.Format
	failure["validation_records"] = result.SupplierRecordCount
	failure["validation_parsed_records"] = result.ParsedCount
	failure["validation_invalid_records"] = result.InvalidRecordCount
	failure["validation_detail"] = fmt.Sprintf("%s: format=%s records=%d parsed=%d invalid=%d; counts are a stream summary, not line locations", invalid.Code, result.Format, result.SupplierRecordCount, result.ParsedCount, result.InvalidRecordCount)
}
