package sciencecapability

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

const digestFixture = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestEvaluateRequiresRealCompletedEngineWitness(t *testing.T) {
	catalog, err := Decode(strings.NewReader(`{
  "schemaVersion": 2,
  "capabilities": [{
    "id": "molecular-docking",
    "description": "Generate receptor-bound ligand poses",
    "acceptedEngines": [{
      "id": "diffdock",
      "requiredInputs": ["receptor", "ligand"],
      "scoreKinds": ["pose-confidence"],
      "requiredArtifacts": ["ranked-pose", "execution-log"],
      "requiresWeights": true,
      "executionPack": {"id":"molecular-docking.diffdock","mode":"unavailable","skill":"diffdock","unavailableReason":"fixture is unavailable"}
    }]
  }]
}`))
	if err != nil {
		t.Fatal(err)
	}
	witness := Witness{
		Version: WitnessVersion, Capability: "molecular-docking", Engine: "diffdock",
		EnginePackage: "diffdock",
		EngineVersion: "9a22cbcbc761", ProfileSHA256: digestFixture, CodeSHA256: digestFixture, WeightsSHA256: digestFixture,
		EnvironmentSHA256: digestFixture, Provider: "byoc:modal", JobID: "job-1", State: "completed",
		ScoreKind: "pose-confidence", Inputs: map[string]string{"receptor": digestFixture, "ligand": digestFixture},
		Artifacts:   []ArtifactWitness{{Kind: "ranked-pose", SHA256: digestFixture}, {Kind: "execution-log", SHA256: digestFixture}},
		CompletedAt: time.Now().UTC(),
	}
	result := Evaluate(catalog, []string{"molecular-docking"}, []Witness{witness})
	if len(result.Satisfied) != 1 || len(result.Missing) != 0 || len(result.Invalid) != 0 {
		t.Fatalf("evaluation=%#v", result)
	}

	witness.ScoreKind = "heuristic-property-score"
	result = Evaluate(catalog, []string{"molecular-docking"}, []Witness{witness})
	if len(result.Satisfied) != 0 || len(result.Missing) != 1 || len(result.Invalid) != 1 || result.Invalid[0] != "molecular-docking:invalid_score_kind" {
		t.Fatalf("heuristic evaluation=%#v", result)
	}
}

func TestDecodeFailsClosedOnUnknownAndDuplicateContractFields(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown":   `{"schemaVersion":2,"capabilities":[],"fallback":true}`,
		"duplicate": `{"schemaVersion":2,"capabilities":[{"id":"molecular-docking","description":"a","acceptedEngines":[{"id":"diffdock","requiredInputs":["receptor","ligand"],"scoreKinds":["pose-confidence"],"requiredArtifacts":["ranked-pose"],"requiresWeights":true,"executionPack":{"id":"molecular-docking.diffdock","mode":"unavailable","skill":"diffdock","unavailableReason":"fixture unavailable"}}]},{"id":"molecular-docking","description":"b","acceptedEngines":[{"id":"vina","requiredInputs":["receptor","ligand"],"scoreKinds":["affinity-kcal-mol"],"requiredArtifacts":["ranked-pose"],"requiresWeights":false,"executionPack":{"id":"molecular-docking.vina","mode":"unavailable","skill":"autodock-vina","unavailableReason":"fixture unavailable"}}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(raw)); err == nil {
				t.Fatal("expected closed contract rejection")
			}
		})
	}
}

func TestValidateWitnessRejectsDuplicateArtifactsFutureCompletionAndOptionalDigestDrift(t *testing.T) {
	catalog, err := Decode(strings.NewReader(`{
  "schemaVersion": 2,
  "capabilities": [{
    "id": "molecular-docking",
    "description": "Generate receptor-bound ligand poses",
    "acceptedEngines": [{
      "id": "autodock-vina",
      "requiredInputs": ["receptor", "ligand"],
      "scoreKinds": ["affinity-kcal-mol"],
      "requiredArtifacts": ["ranked-pose", "execution-log"],
      "requiresWeights": false,
      "executionPack": {"id":"molecular-docking.autodock-vina","mode":"unavailable","skill":"autodock-vina","unavailableReason":"fixture is unavailable"}
    }]
  }]
}`))
	if err != nil {
		t.Fatal(err)
	}
	valid := Witness{
		Version: WitnessVersion, Capability: "molecular-docking", Engine: "autodock-vina",
		EnginePackage: "vina",
		EngineVersion: "1.2.7", ProfileSHA256: digestFixture, CodeSHA256: digestFixture, EnvironmentSHA256: digestFixture,
		Provider: "byoc:modal", JobID: "job-1", State: "completed", ScoreKind: "affinity-kcal-mol",
		Inputs: map[string]string{"receptor": digestFixture, "ligand": digestFixture},
		Artifacts: []ArtifactWitness{
			{Kind: "ranked-pose", SHA256: digestFixture},
			{Kind: "execution-log", SHA256: digestFixture},
		},
		CompletedAt: time.Now().UTC(),
	}
	for name, mutate := range map[string]func(*Witness){
		"duplicate-artifact": func(witness *Witness) {
			witness.Artifacts = append(witness.Artifacts, witness.Artifacts[0])
		},
		"future-completion": func(witness *Witness) {
			witness.CompletedAt = time.Now().UTC().Add(time.Hour)
		},
		"invalid-optional-weights": func(witness *Witness) {
			witness.WeightsSHA256 = "not-a-digest"
		},
		"control-character-identity": func(witness *Witness) {
			witness.JobID = "job\nsecret"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Artifacts = append([]ArtifactWitness(nil), valid.Artifacts...)
			mutate(&candidate)
			if err := ValidateWitness(catalog, candidate); err == nil {
				t.Fatal("expected closed witness rejection")
			}
		})
	}
}

func TestRepositoryScientificCapabilityCatalogIsValid(t *testing.T) {
	catalog, err := DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Capabilities) != 9 {
		t.Fatalf("capabilities=%#v", catalog.Capabilities)
	}
}

func TestExecutionParameterEvidenceContractFailsClosed(t *testing.T) {
	raw, err := bundledExecutionPacks.ReadFile("scientific-capabilities.v2.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string][]byte{
		"unknown evidence authority": bytes.Replace(raw, []byte(`"evidence": "resolved-user-input"`), []byte(`"evidence": "model-prose"`), 1),
		"unknown runtime authority":  bytes.Replace(raw, []byte(`"evidence": "runtime-response-language"`), []byte(`"evidence": "model-response-language"`), 1),
		"unknown resolver authority": bytes.Replace(raw, []byte(`"evidence": "selected-evidence-resolver"`), []byte(`"evidence": "selected-engine"`), 1),
		"insecure registered download": bytes.Replace(
			raw, []byte(`"url": "https://github.com/rdk/p2rank/`), []byte(`"url": "http://github.com/rdk/p2rank/`), 1,
		),
		"invalid registered download digest": bytes.Replace(
			raw, []byte(`"sha256": "d243f2d9036ac053fefb9407b5fe1c85f4fe077c519fd975ac585e995feab274"`),
			[]byte(`"sha256": "unverified"`), 1,
		),
		"invalid registered redirect host": bytes.Replace(
			raw, []byte(`"redirectHosts": ["release-assets.githubusercontent.com"]`),
			[]byte(`"redirectHosts": ["release-assets.githubusercontent.com/path"]`), 1,
		),
		"invalid execution output delivery": bytes.Replace(
			raw, []byte(`"delivery": "snapshot"`), []byte(`"delivery": "model-selected"`), 1,
		),
		"unsafe evidence group": bytes.Replace(raw, []byte(`"evidenceGroup": "binding-site-center"`), []byte(`"evidenceGroup": "../center"`), 1),
		"missing evidence group": bytes.Replace(
			raw, []byte(`, "evidenceGroup": "binding-site-center"`), nil, 1,
		),
		"unsupported boolean evidence": bytes.Replace(
			raw,
			[]byte(`"name": "center_x", "argument": "--center-x", "type": "number"`),
			[]byte(`"name": "center_x", "argument": "--center-x", "type": "boolean"`),
			1,
		),
		"missing evidence resolver target": bytes.Replace(
			raw,
			[]byte(`"skill": "p2rank-pocket-detection",`),
			[]byte(`"skill": "missing-pocket-resolver",`),
			1,
		),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(bytes.NewReader(mutation)); err == nil {
				t.Fatal("invalid execution parameter evidence contract was accepted")
			}
		})
	}
}

func TestLocalExecutionPackDerivesItsHarnessBoundaryFromRegistry(t *testing.T) {
	catalog := Catalog{Capabilities: []Definition{{
		ID: "example-capability",
		AcceptedEngines: []EngineDefinition{
			{
				ID: "engine-a", Package: "engine-package",
				ExecutionPack: ExecutionPack{
					ID: "example-capability.engine-a", Mode: "local", Skill: "managed-workflow",
					Script: "executionpacks/run.py", Imports: []string{"examplelib.submodule"},
					CLIWitnesses: []ExecutionCLIWitness{{Executable: "example-cli"}},
				},
			},
			{
				ID:            "remote-engine",
				ExecutionPack: ExecutionPack{ID: "example-capability.remote-engine", Mode: "unavailable", Skill: "managed-workflow"},
			},
		},
	}}}
	packs := catalog.LocalExecutionPacksForSkill("MANAGED-WORKFLOW")
	if len(packs) != 1 || packs[0].ExecutionPack.ID != "example-capability.engine-a" {
		t.Fatalf("local execution packs=%#v", packs)
	}
	wantIdentifiers := []string{"engine-a", "engine-package", "example-cli", "examplelib", "examplelib.submodule"}
	if got := packs[0].ManagedExecutionIdentifiers(); strings.Join(got, ",") != strings.Join(wantIdentifiers, ",") {
		t.Fatalf("managed identifiers=%v want=%v", got, wantIdentifiers)
	}
	if got := packs[0].ExecutionPack.MaterializedSkillEntrypoint(); got != "scripts/run.py" {
		t.Fatalf("materialized entrypoint=%q", got)
	}
}
