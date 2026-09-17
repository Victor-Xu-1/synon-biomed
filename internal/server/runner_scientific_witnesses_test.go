package server

import (
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
)

func TestTrustedScientificCapabilityWitnessesRestoreOnlyFromCompletedDurableEvidence(t *testing.T) {
	witness := scientificCapabilityTestWitness(
		"molecular-docking", "autodock-vina", "vina", "affinity-kcal-mol", false,
	)
	failedWitness := witness
	failedWitness.JobID = "failed-job"
	entries := []eventjournal.Entry{
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			trustedScientificCapabilityWitnessesField: []any{witness},
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "failed", "toolPhase": "failed",
			trustedScientificCapabilityWitnessesField: []any{failedWitness},
		}},
		{Message: eventjournal.Message{
			"type": runnerTrustedScientificEvidenceReplayType, "status": "completed",
			trustedScientificCapabilityWitnessesField: []any{witness},
		}},
	}
	restored := trustedScientificCapabilityWitnessesFromRunnerEntries(entries)
	if len(restored) != 1 || restored[0].JobID != witness.JobID || restored[0].EnginePackage != "vina" {
		t.Fatalf("restored=%#v", restored)
	}
	restored[0].Inputs["receptor"] = "mutated"
	restoredAgain := trustedScientificCapabilityWitnessesFromRunnerEntries(entries)
	if restoredAgain[0].Inputs["receptor"] == "mutated" {
		t.Fatal("durable witness restoration leaked a mutable input map")
	}
}

func TestTrustedScientificCapabilityWitnessSnapshotIsDeepClone(t *testing.T) {
	witness := scientificCapabilityTestWitness(
		"molecular-docking", "autodock-vina", "vina", "affinity-kcal-mol", false,
	)
	run := &sessionRunnerChatRun{}
	run.addTrustedScientificCapabilityWitnesses(witness, witness)
	snapshot := run.trustedScientificCapabilityWitnessesSnapshot()
	if len(snapshot) != 1 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	snapshot[0].Inputs["receptor"] = "mutated"
	snapshot[0].Artifacts[0].SHA256 = "mutated"
	fresh := run.trustedScientificCapabilityWitnessesSnapshot()
	if fresh[0].Inputs["receptor"] == "mutated" || fresh[0].Artifacts[0].SHA256 == "mutated" {
		t.Fatalf("internal witness state was mutated: %#v", fresh)
	}
}

func TestDecodeScientificCapabilityWitnessesRejectsUnknownFields(t *testing.T) {
	raw := []any{map[string]any{
		"version": "synon.scientific-capability-witness.v1", "capability": "molecular-docking",
		"unknown": true,
	}}
	if _, err := decodeScientificCapabilityWitnesses(raw); err == nil {
		t.Fatal("unknown durable witness fields must be rejected")
	}
}
