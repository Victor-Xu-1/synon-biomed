package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	"synon-go/internal/sciencecapability"
)

const trustedScientificCapabilityWitnessesField = "trustedScientificCapabilityWitnesses"

func (run *sessionRunnerChatRun) addTrustedScientificCapabilityWitnesses(witnesses ...sciencecapability.Witness) {
	if run == nil || len(witnesses) == 0 {
		return
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	run.TrustedScientificCapabilityWitnesses = uniqueScientificCapabilityWitnesses(append(
		append([]sciencecapability.Witness(nil), run.TrustedScientificCapabilityWitnesses...), witnesses...,
	))
}

func (run *sessionRunnerChatRun) trustedScientificCapabilityWitnessesSnapshot() []sciencecapability.Witness {
	if run == nil {
		return nil
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	return cloneScientificCapabilityWitnesses(run.TrustedScientificCapabilityWitnesses)
}

func trustedScientificCapabilityWitnessesFromRunnerEntries(entries []eventjournal.Entry) []sciencecapability.Witness {
	result := make([]sciencecapability.Witness, 0)
	for _, entry := range entries {
		message := entry.Message
		if stringValue(message["type"]) != runnerTrustedScientificEvidenceReplayType {
			if stringValue(message["type"]) != "runner_checkpoint" ||
				stringValue(message["status"]) != "completed" ||
				stringValue(message["toolPhase"]) != "completed" {
				continue
			}
			if rawResult, recorded := message["toolResult"]; recorded &&
				agentruntime.ClassifyToolResult(rawResult) != agentruntime.ToolResultSucceeded {
				continue
			}
		}
		witnesses, err := decodeScientificCapabilityWitnesses(message[trustedScientificCapabilityWitnessesField])
		if err != nil {
			continue
		}
		result = append(result, witnesses...)
	}
	return uniqueScientificCapabilityWitnesses(result)
}

func decodeScientificCapabilityWitnesses(raw any) ([]sciencecapability.Witness, error) {
	if raw == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil || len(encoded) == 0 || len(encoded) > 256<<10 {
		return nil, errors.New("scientific capability witness payload is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var witnesses []sciencecapability.Witness
	if err := decoder.Decode(&witnesses); err != nil {
		return nil, errors.New("scientific capability witness payload is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("scientific capability witness payload has trailing data")
	}
	return cloneScientificCapabilityWitnesses(witnesses), nil
}

func uniqueScientificCapabilityWitnesses(values []sciencecapability.Witness) []sciencecapability.Witness {
	result := make([]sciencecapability.Witness, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, witness := range values {
		encoded, err := json.Marshal(witness)
		if err != nil {
			continue
		}
		key := string(encoded)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, witness)
	}
	sort.Slice(result, func(left, right int) bool {
		leftKey := result[left].Capability + "\x00" + result[left].JobID + "\x00" + result[left].ProfileSHA256
		rightKey := result[right].Capability + "\x00" + result[right].JobID + "\x00" + result[right].ProfileSHA256
		return leftKey < rightKey
	})
	return cloneScientificCapabilityWitnesses(result)
}

func cloneScientificCapabilityWitnesses(values []sciencecapability.Witness) []sciencecapability.Witness {
	result := make([]sciencecapability.Witness, len(values))
	for index, witness := range values {
		result[index] = witness
		result[index].Inputs = make(map[string]string, len(witness.Inputs))
		for kind, digest := range witness.Inputs {
			result[index].Inputs[kind] = digest
		}
		result[index].Artifacts = append([]sciencecapability.ArtifactWitness(nil), witness.Artifacts...)
	}
	return result
}
