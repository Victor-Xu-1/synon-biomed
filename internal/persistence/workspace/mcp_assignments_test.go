package workspace

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMCPAssignmentsAndOAuthStatusAreUserScoped(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateAgent(CreateAgentInput{ID: "agent-1", UserID: "user-1", Name: "research", DisplayName: "Research", Description: "Research", SystemPrompt: "Research"}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	server, err := store.CreateMCPServer(MCPServerInput{ID: "mcp-1", UserID: "user-1", Name: "evidence", URL: "https://mcp.example.test", Transport: "streamable-http"})
	if err != nil {
		t.Fatalf("create mcp server: %v", err)
	}
	assignment, err := store.AssignMCPServerToAgent(MCPAssignmentInput{ID: "assignment-1", MCPServerID: server.ID, UserID: "user-1", AgentName: "research"})
	if err != nil {
		t.Fatalf("assign mcp server: %v", err)
	}
	if assignment.AgentName != "research" {
		t.Fatalf("assignment = %#v", assignment)
	}
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	if err := store.UpsertMCPOAuthStatus(MCPOAuthStatusInput{MCPServerID: server.ID, UserID: "user-1", AccessTokenRef: "secret://mcp/evidence", TokenType: "Bearer", ExpiresAt: &expires, Scopes: []string{"tools.read"}}); err != nil {
		t.Fatalf("upsert oauth status: %v", err)
	}
	status, ok, err := store.GetMCPOAuthStatus(server.ID, "user-1")
	if err != nil {
		t.Fatalf("get oauth status: %v", err)
	}
	if !ok || status.AccessTokenRef != "secret://mcp/evidence" || len(status.Scopes) != 1 {
		t.Fatalf("oauth status = %#v, ok=%v", status, ok)
	}
	if err := store.DisconnectMCPOAuth(server.ID, "user-1"); err != nil {
		t.Fatalf("disconnect oauth: %v", err)
	}
	if _, ok, err := store.GetMCPOAuthStatus(server.ID, "user-1"); err != nil || ok {
		t.Fatalf("oauth status after disconnect = ok:%v err:%v", ok, err)
	}
}
