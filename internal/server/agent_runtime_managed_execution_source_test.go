package server

import (
	"os"
	"path/filepath"
	"testing"

	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func TestManagedExecutionSourceResolvesExtensionlessLauncherAndWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "engine"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "engine", "launcher"), []byte("#!/usr/bin/env bash\nRUNNER=reviewed-engine\n\"$RUNNER\" \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	kernel := &agentKernelContext{workspaceDir: root}
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "engine-skill"})
	run := &sessionRunnerChatRun{}
	run.addExecutedSkillNames("engine-skill")
	gateway := serverAgentRuntimeToolGateway{kernel: kernel, taskRun: run, server: &Server{
		skillCatalog: catalog, scienceCapabilities: &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
			ID: "capability", AcceptedEngines: []sciencecapability.EngineDefinition{{ID: "reviewed-engine", ExecutionPack: sciencecapability.ExecutionPack{
				ID: "capability.reviewed-engine", Mode: "local", Skill: "engine-skill", Script: "executionpacks/engine.py",
			}}},
		}}},
	}}
	for _, command := range []string{"cd engine && ./launcher predict --input ../protein.pdb", "./engine/launcher predict"} {
		identity, source, found := managedExecutionSourceIdentifier(command, []string{"reviewed-engine"}, kernel)
		if !found || identity != "reviewed-engine" || source != "engine/launcher" {
			t.Fatalf("extensionless owned entrypoint escaped canonical routing: %q %q %t", identity, source, found)
		}
		result := gateway.agentRuntimeManagedExecutionPackPreflight("bash", map[string]any{"command": command})
		if result["status"] != "skill_execution_entrypoint_required" || result["executed"] != false || result["required_entrypoint"] != "scripts/engine.py" {
			t.Fatalf("resolved launcher bypassed the existing execution-pack gateway: %#v", result)
		}
	}
	if _, _, found := managedExecutionSourceIdentifier("echo './engine/launcher'", []string{"reviewed-engine"}, kernel); found {
		t.Fatal("a path printed as data became a process invocation")
	}
	outside := filepath.Join(t.TempDir(), "launcher")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\nreviewed-engine\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "foreign")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, found := managedExecutionSourceIdentifier("./foreign", []string{"reviewed-engine"}, kernel); found {
		t.Fatal("foreign filesystem content became task execution authority")
	}
}
