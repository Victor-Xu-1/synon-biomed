package workspace

import (
	"path/filepath"
	"testing"
)

func TestFramesPreserveRootOrderAndAgentsAreUserScoped(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	root, err := store.CreateFrame(CreateFrameInput{ID: "frame-root", ProjectID: project.ID, AgentName: "research", Status: "running", ConversationType: "task", Name: "Root"})
	if err != nil {
		t.Fatalf("create root frame: %v", err)
	}
	child, err := store.CreateFrame(CreateFrameInput{ID: "frame-child", ProjectID: project.ID, ParentFrameID: root.ID, AgentName: "research", Status: "queued", ConversationType: "delegate"})
	if err != nil {
		t.Fatalf("create child frame: %v", err)
	}
	if root.RootFrameID != root.ID || root.RootSequence != 0 {
		t.Fatalf("root identity = %#v", root)
	}
	if child.RootFrameID != root.ID || child.RootSequence != 1 {
		t.Fatalf("child lineage = %#v", child)
	}

	agent, err := store.CreateAgent(CreateAgentInput{ID: "agent-1", UserID: "user-1", Name: "research", DisplayName: "Research", Description: "Find evidence", SystemPrompt: "Be precise", SkillNames: []string{"web-search"}})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if !agent.Enabled || len(agent.SkillNames) != 1 || agent.SkillNames[0] != "web-search" {
		t.Fatalf("unexpected agent: %#v", agent)
	}
	if _, err := store.CreateAgent(CreateAgentInput{ID: "agent-2", UserID: "user-1", Name: "research", DisplayName: "Duplicate", Description: "duplicate", SystemPrompt: "duplicate"}); err == nil {
		t.Fatal("duplicate user agent name was accepted")
	}
	if _, err := store.CreateAgent(CreateAgentInput{ID: "agent-3", UserID: "user-2", Name: "research", DisplayName: "Other", Description: "other", SystemPrompt: "other"}); err != nil {
		t.Fatalf("same agent name for different user: %v", err)
	}
}
