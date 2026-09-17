package server

import (
	"context"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/tools/mcpstdio"
)

func TestKernelMCPApprovalPublishesAuthorityBeforeImmediateDecision(t *testing.T) {
	for _, approved := range []bool{true, false} {
		name := "denied"
		if approved {
			name = "approved"
		}
		t.Run(name, func(t *testing.T) {
			app := New(Options{FileRoot: t.TempDir()})
			t.Cleanup(func() { closeTestServer(t, app) })
			if _, err := app.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{"mode": "ask"}); err != nil {
				t.Fatal(err)
			}
			resolution := kernelMCPResolution{
				connector: workspaceMCPRuntimeConnector{ID: "custom:remote", Name: "remote", Source: "custom"},
				tool:      mcpstdio.ToolProjection{Name: "mcp__remote__echo", ToolName: "echo"},
			}
			permission := app.kernelMCPPermissionResult(context.Background(), "frame", resolution,
				agentruntime.ToolCall{ID: "immediate-decision", Name: resolution.tool.Name}, map[string]any{"text": "evidence"})
			approvalID := stringValue(permission["approvalId"])
			if approvalID == "" {
				t.Fatalf("approval was not published: %#v", permission)
			}
			entry, found, err := app.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
			if err != nil || !found {
				t.Fatalf("approval found=%t err=%v", found, err)
			}
			// The first visible version must be complete. A second annotation
			// write admits a window where an immediate decision is overwritten.
			if entry.Version != 1 {
				t.Fatalf("approval authority was published in %d writes, want one atomic publication", entry.Version)
			}
			value := mapValue(entry.Value)
			for key, want := range map[string]string{
				"status": "pending", "approvalSource": "kernel-host-mcp", "frameId": "frame",
				"mcpServerId": "custom:remote", "mcpServer": "remote", "mcpTool": "echo", "kernelKind": "operon",
			} {
				if stringValue(value[key]) != want {
					t.Fatalf("first publication %s=%v want=%s", key, value[key], want)
				}
			}
			decision, err := app.resolveAgentRuntimeApprovalMessage(context.Background(), "", map[string]any{
				"approvalId": approvalID, "approve": approved,
			})
			if err != nil || stringValue(mapValue(decision)["status"]) != name {
				t.Fatalf("immediate decision=%#v err=%v", decision, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = app.waitForKernelMCPApproval(ctx, approvalID)
			if approved && err != nil || !approved && err == nil || ctx.Err() != nil {
				t.Fatalf("immediate %s was not retained: err=%v ctx=%v", name, err, ctx.Err())
			}
		})
	}
}
