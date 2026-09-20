package server

import (
	"context"
	"reflect"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/compute"
	"synon-go/internal/skills"
)

func TestManagedEnvironmentPreflightCapabilitiesBelongToRequestedImplementation(t *testing.T) {
	for _, tc := range []struct {
		name, implementation string
		loaded               []string
		capabilities         []string
		feasible             bool
		contractSkill        string
	}{
		{"cpu stage after gpu stage", "CPU Engine", []string{"cpu-stage", "gpu-stage"}, []string{"cpu-analysis"}, true, "cpu-stage"},
		{"exact skill alias", "cpu-stage", []string{"gpu-stage"}, []string{"cpu-analysis"}, true, "cpu-stage"},
		{"unknown composite", "CPU Engine+GPU Engine", []string{"cpu-stage", "gpu-stage"}, nil, true, ""},
		{"unknown engine", "Unregistered Engine", []string{"gpu-stage"}, nil, true, ""},
		{"registered gpu", "GPU Engine", []string{"cpu-stage", "gpu-stage"}, []string{"gpu", "gpu-analysis"}, false, "gpu-stage"},
		{"registered gpu before skill load", "GPU Engine", []string{"cpu-stage"}, []string{"gpu", "gpu-analysis"}, false, "gpu-stage"},
		{"registered composition", "Combined Engine", []string{"cpu-stage"}, []string{"cpu-analysis", "gpu", "gpu-analysis"}, false, "combined-stage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, identity := managedEnvironmentToolFixture(t)
			server.skillCatalog = skills.NewCatalog()
			server.skillCatalog.AddSkill(skills.Skill{Name: "cpu-stage", ImplementationIdentities: []string{"CPU Engine"}, RequiredCapabilities: []string{"cpu-analysis"}})
			server.skillCatalog.AddSkill(skills.Skill{Name: "gpu-stage", ImplementationIdentities: []string{"GPU Engine"}, RequiredCapabilities: []string{"gpu-analysis", "gpu"}})
			server.skillCatalog.AddSkill(skills.Skill{Name: "combined-stage", ImplementationIdentities: []string{"Combined Engine"}, RequiredCapabilities: []string{"cpu-analysis", "gpu-analysis", "gpu"}})
			server.hostGPUDetector = func(context.Context) compute.GPUInfo { return compute.UnavailableGPUInfo() }
			required := []string{"cpu-analysis", "gpu-analysis"}
			run := &sessionRunnerChatRun{ExecutedSkillNames: tc.loaded, RequiredScientificCapabilities: append([]string(nil), required...)}
			authority := &recordingManagedEnvironmentAuthority{}
			result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
				withTranscriptRunnerChatRun(context.Background(), run), identity,
				agentruntime.ToolCall{ID: "implementation-capability-scope"}, manageEnvironmentsToolName,
				map[string]any{"mode": "preflight", "name": "analysis", "implementation": tc.implementation,
					"packages": []any{"fixture-library"}, "resource_requirements": managedEnvironmentTestResources(),
					"human_description": "Check the current implementation without installing it"}, authority,
			)
			if err != nil {
				t.Fatal(err)
			}
			value := mapValue(result)
			caps := stringArrayValue(value["required_capabilities"])
			if len(caps) != len(tc.capabilities) || (len(caps) > 0 && !reflect.DeepEqual(caps, tc.capabilities)) {
				t.Errorf("capability receipt inherited unrelated loaded skills: got=%v want=%v", caps, tc.capabilities)
			}
			if compatibilityPlanBool(value["feasible"]) != tc.feasible {
				t.Errorf("implementation-specific feasibility=%v want=%v blockers=%v", value["feasible"], tc.feasible, value["blockers"])
			}
			if stringValue(value["capability_contract_skill"]) != tc.contractSkill {
				t.Errorf("capability provenance=%v want=%s", value["capability_contract_skill"], tc.contractSkill)
			}
			accelerator := ""
			if tc.contractSkill != "" {
				accelerator = "none"
				if !tc.feasible {
					accelerator = "required"
				}
			}
			if stringValue(value["verified_accelerator_requirement"]) != accelerator {
				t.Errorf("verified accelerator=%v want=%s", value["verified_accelerator_requirement"], accelerator)
			}
			if authority.mutations != 0 || len(run.selectedImplementationsSnapshot()) != 0 ||
				!reflect.DeepEqual(run.requiredScientificCapabilitiesSnapshot(), required) {
				t.Fatal("read-only preflight changed implementation selection, task requirements, or environment")
			}
		})
	}
}

func TestImplementationResourceRequirementsRetainExplicitResourceRequest(t *testing.T) {
	server := &Server{skillCatalog: skills.NewCatalog()}
	server.skillCatalog.AddSkill(skills.Skill{Name: "cpu-stage", ImplementationIdentities: []string{"CPU Engine"}, RequiredCapabilities: []string{"cpu-analysis"}})
	requirements := managedEnvironmentResourceRequirements{MinCPUCores: 8, MinMemoryMB: 2048, MinDiskMB: 4096, Accelerator: "required", MinAcceleratorMemoryMB: 1024}
	for _, implementation := range []string{"CPU Engine", "Unknown Engine", ""} {
		got, _, _ := server.applyImplementationResourceRequirements(implementation, requirements)
		if got != requirements {
			t.Fatalf("explicit resource request changed for %q: got=%#v want=%#v", implementation, got, requirements)
		}
	}
	var unavailable *Server
	got, capabilities, skill := unavailable.applyImplementationResourceRequirements("GPU Engine", requirements)
	if got != requirements || len(capabilities) != 0 || skill != "" {
		t.Fatalf("unavailable catalog invented authority: requirements=%#v capabilities=%v skill=%s", got, capabilities, skill)
	}
}
