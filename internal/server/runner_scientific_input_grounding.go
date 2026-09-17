package server

import (
	"encoding/json"
	"io"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/sciencecapability"
)

const (
	trustedScientificUserArtifactDigestSignalPrefix = "input-digest:user-artifact:"
	managedExecutionPackInputReceiptPrefix          = "SYNON_EXECUTION_PACK_INPUT_RECEIPT="
)

func trustedScientificUserArtifactInputSignals(refs []transcriptstore.UserArtifactReference) []string {
	if len(refs) == 0 {
		return nil
	}
	signals := []string{trustedScientificUserArtifactInputSignal}
	for _, ref := range refs {
		checksum := strings.ToLower(strings.TrimSpace(ref.Checksum))
		if strings.TrimSpace(ref.VersionID) != "" && isSHA256Hex(checksum) {
			signals = append(signals, trustedScientificUserArtifactDigestSignalPrefix+checksum)
		}
	}
	return uniqueSortedFolded(signals)
}

func trustedScientificResultInputDigests(rawResult any) []string {
	result, ok := softwareRuntimeObjectReceipt(rawResult)
	if !ok {
		return nil
	}
	digests := make([]string, 0)
	if witness, witnessOK := softwareRuntimeObjectReceipt(result["scientific_witness"]); witnessOK {
		if inputs, inputsOK := softwareRuntimeObjectReceipt(witness["inputs"]); inputsOK {
			for _, value := range inputs {
				checksum := strings.ToLower(strings.TrimSpace(stringValue(value)))
				if isSHA256Hex(checksum) {
					digests = append(digests, checksum)
				}
			}
		}
	}
	return uniqueSortedFolded(digests)
}

func (s *Server) trustedScientificManagedPackInputDigests(
	run *sessionRunnerChatRun,
	toolName string,
	rawInput any,
	rawResult any,
) []string {
	if normalizeAgentToolName(toolName) != "bash" || s == nil || s.skillCatalog == nil ||
		s.scienceCapabilities == nil || run == nil {
		return nil
	}
	input, _ := rawInput.(map[string]any)
	command := strings.TrimSpace(stringValue(input["command"]))
	result, ok := softwareRuntimeObjectReceipt(rawResult)
	if command == "" || !ok {
		return nil
	}
	skillNames := run.executedSkillNamesSnapshot()
	for _, implementation := range run.selectedImplementationsSnapshot() {
		if skill, found := dedicatedSkillForImplementation(s.skillCatalog, implementation); found {
			skillNames = append(skillNames, skill.Name)
		}
	}
	for _, skillName := range uniqueSortedFolded(skillNames) {
		skill, found := findCatalogSkill(s.skillCatalog, skillName)
		if !found {
			continue
		}
		for _, engine := range s.scienceCapabilities.LocalExecutionPacksForSkill(skill.Name) {
			pack := engine.ExecutionPack
			if !commandExecutesManagedExecutionPack(skill.Name, pack, command) {
				continue
			}
			if digests, valid := managedExecutionPackInputDigestsFromStdout(stringValue(result["stdout"]), pack.ID, pack.Inputs); valid {
				return digests
			}
		}
	}
	return nil
}

func managedExecutionPackInputDigestsFromStdout(stdout, packID string, inputs []sciencecapability.ExecutionInput) ([]string, bool) {
	var encoded string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, managedExecutionPackInputReceiptPrefix) {
			continue
		}
		if encoded != "" {
			return nil, false
		}
		encoded = strings.TrimPrefix(line, managedExecutionPackInputReceiptPrefix)
	}
	if encoded == "" {
		return nil, false
	}
	var receipt struct {
		Schema          string             `json:"schema"`
		ExecutionPackID string             `json:"execution_pack_id"`
		Inputs          map[string]*string `json:"inputs"`
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		receipt.Schema != "synon.execution-pack-input-receipt.v1" || receipt.ExecutionPackID != packID ||
		len(receipt.Inputs) < len(inputs) {
		return nil, false
	}
	required := make(map[string]bool, len(inputs))
	digests := make([]string, 0, len(receipt.Inputs))
	for _, input := range inputs {
		value, found := receipt.Inputs[input.Kind]
		if !found || value == nil {
			return nil, false
		}
		checksum := strings.ToLower(strings.TrimSpace(*value))
		if !isSHA256Hex(checksum) {
			return nil, false
		}
		required[input.Kind] = true
		digests = append(digests, checksum)
	}
	// A governed execution pack may include optional evidence inputs in the
	// same receipt (for example, a validated selector and its validation
	// record). They are absent as JSON null when unused. Accept those additive
	// fields without weakening the mandatory registered-input check, and reject
	// any populated value that is not itself a content digest.
	for kind, value := range receipt.Inputs {
		if required[kind] || value == nil {
			continue
		}
		checksum := strings.ToLower(strings.TrimSpace(*value))
		if !isSHA256Hex(checksum) {
			return nil, false
		}
		digests = append(digests, checksum)
	}
	return uniqueSortedFolded(digests), true
}

func (s *Server) trustedScientificExecutionConsumesUserArtifact(
	run *sessionRunnerChatRun,
	priorSignals []string,
	toolName string,
	input any,
	result any,
) bool {
	digests := trustedScientificResultInputDigests(result)
	digests = append(digests, s.trustedScientificManagedPackInputDigests(run, toolName, input, result)...)
	return trustedScientificInputDigestsMatchSignals(priorSignals, digests)
}

func trustedScientificInputDigestsMatchSignals(priorSignals, digests []string) bool {
	if len(digests) == 0 {
		return false
	}
	wanted := foldedSet(digests...)
	for _, signal := range normalizeTrustedScientificReviewSignals(priorSignals) {
		if !strings.HasPrefix(signal, trustedScientificUserArtifactDigestSignalPrefix) {
			continue
		}
		if foldedSetContains(wanted, strings.TrimPrefix(signal, trustedScientificUserArtifactDigestSignalPrefix)) {
			return true
		}
	}
	return false
}
