package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestKernelSupervisionPersistsCapMappingAndMessageCounts(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "root-1", ProjectID: "project-1", AgentName: "GENERAL", Status: "processing", ConversationType: "main", Name: "Root"}); err != nil {
		t.Fatal(err)
	}
	requests := make([]KernelDelegateRequest, 47)
	for index := range requests {
		requests[index] = KernelDelegateRequest{Task: "task", Name: "worker", Model: "saved-model"}
	}
	children, err := store.CreateKernelSupervisedChildren(context.Background(), CreateKernelDelegatesInput{
		ParentFrameID: "root-1", OwnerUserID: "user-1", ToolUseID: "tool-wave-1",
		Requests: requests, SpawnCap: 48,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 47 {
		t.Fatalf("children = %d", len(children))
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("root-1")
	if err != nil || !found {
		t.Fatalf("parent metadata found=%t err=%v", found, err)
	}
	mapping := metadata.ContextData["_tool_id_to_frame_id"].(map[string]any)
	if len(mapping) != 47 || mapping["tool-wave-1:0"] != children[0].FrameID {
		t.Fatalf("tool mapping = %#v", mapping)
	}
	childEvents, err := store.ListFrameEvents(children[0].FrameID, 0, 10)
	if err != nil || len(childEvents) != 1 || childEvents[0].Type != "user_message" {
		t.Fatalf("child events = %#v err=%v", childEvents, err)
	}
	childMetadata, found, err := store.GetFrameRuntimeMetadata(children[0].FrameID)
	if err != nil || !found || childMetadata.InputData["request"] != "task" {
		t.Fatalf("child metadata = %#v found=%t err=%v", childMetadata, found, err)
	}
	parentEvents, err := store.ListFrameEvents("root-1", 0, 10)
	if err != nil || len(parentEvents) != 1 || parentEvents[0].Type != "tool_use" {
		t.Fatalf("parent events = %#v err=%v", parentEvents, err)
	}
	stats, err := store.KernelDelegationStats(context.Background(), "root-1", "user-1", 48)
	if err != nil || stats.SpawnedThisTask != 47 || stats.Remaining != 1 {
		t.Fatalf("stats = %#v err=%v", stats, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateKernelSupervisedChildren(context.Background(), CreateKernelDelegatesInput{
		ParentFrameID: children[0].FrameID, OwnerUserID: "user-1", ToolUseID: "depth-denied",
		Requests: []KernelDelegateRequest{{Task: "grandchild denied"}}, MaxDepth: 1,
	}); err == nil {
		t.Fatal("default max depth allowed a grandchild")
	}
	grandchildren, err := store.CreateKernelSupervisedChildren(context.Background(), CreateKernelDelegatesInput{
		ParentFrameID: children[0].FrameID, OwnerUserID: "user-1", ToolUseID: "depth-two",
		Requests: []KernelDelegateRequest{{Task: "grandchild completed first"}, {Task: "grandchild stopped recursively"}}, SpawnCap: 48, MaxDepth: 2,
	})
	if err != nil || len(grandchildren) != 2 || grandchildren[0].ParentFrameID != children[0].FrameID {
		t.Fatalf("max depth two grandchildren=%#v err=%v", grandchildren, err)
	}
	if _, err := store.CompleteKernelSupervisedChild(context.Background(), grandchildren[0].FrameID, "user-1", "completed", map[string]any{"response": "natural"}, ""); err != nil {
		t.Fatal(err)
	}
	stoppedTree, err := store.StopKernelSupervisedChildTree(context.Background(), "root-1", children[0].FrameID, "user-1", "Parent requested stop")
	if err != nil || !stoppedTree.Applied || len(stoppedTree.StoppedDescendants) != 1 || stoppedTree.StoppedDescendants[0].FrameID != grandchildren[1].FrameID {
		t.Fatalf("atomic stop tree=%#v err=%v", stoppedTree, err)
	}
	if stoppedTree.Target.Status != "completed" || stoppedTree.Target.Output["stopped_by_parent"] != true ||
		stoppedTree.StoppedDescendants[0].Output["stopped_by_parent"] != true {
		t.Fatalf("stopped tree payload=%#v", stoppedTree)
	}
	natural, found, err := store.GetKernelSupervisedChild(context.Background(), children[0].FrameID, grandchildren[0].FrameID, "user-1")
	if err != nil || !found || natural.Output["stopped_by_parent"] != nil || natural.Output["response"] != "natural" {
		t.Fatalf("already-terminal descendant changed=%#v found=%t err=%v", natural, found, err)
	}
	defer store.Close()
	_, err = store.CreateKernelSupervisedChildren(context.Background(), CreateKernelDelegatesInput{
		ParentFrameID: "root-1", OwnerUserID: "user-1", ToolUseID: "tool-wave-2",
		Requests: []KernelDelegateRequest{{Task: "must reject"}}, SpawnCap: 48,
	})
	if err == nil {
		t.Fatal("restart reset the durable spawn cap")
	}
	frames, err := store.ListFrames("project-1", 100, 0)
	if err != nil || len(frames) != 50 {
		t.Fatalf("frames after cap reject = %d err=%v", len(frames), err)
	}
}

func TestKernelSupervisionTopologyTerminalIdempotencyAndArtifacts(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, project := range []CreateProjectInput{
		{ID: "project-1", UserID: "user-1", Name: "One"},
		{ID: "project-2", UserID: "user-2", Name: "Two"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	for _, frame := range []CreateFrameInput{
		{ID: "root-1", ProjectID: "project-1", AgentName: "GENERAL", Status: "processing", ConversationType: "main"},
		{ID: "root-2", ProjectID: "project-2", AgentName: "GENERAL", Status: "processing", ConversationType: "main"},
	} {
		if _, err := store.CreateFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	children, err := store.CreateKernelSupervisedChildren(context.Background(), CreateKernelDelegatesInput{
		ParentFrameID: "root-1", OwnerUserID: "user-1", ToolUseID: "tool-1",
		Requests: []KernelDelegateRequest{{Task: "first", Name: "first"}, {Task: "second", Name: "second"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SendKernelSupervisionMessage(context.Background(), children[0].FrameID, children[1].FrameID, "user-1", "sibling denied", "info"); err == nil {
		t.Fatal("sibling topology was allowed")
	}
	if _, _, err := store.SendKernelSupervisionMessage(context.Background(), "root-2", children[0].FrameID, "user-2", "cross-user denied", "info"); err == nil {
		t.Fatal("cross-user topology was allowed")
	}
	if child, relation, err := store.SendKernelSupervisionMessage(context.Background(), "root-1", children[0].FrameID, "user-1", "direct child", "question"); err != nil || relation != "child" || child.FrameID != children[0].FrameID {
		t.Fatalf("direct child send child=%#v relation=%q err=%v", child, relation, err)
	}
	pendingMessages, err := store.ListPendingKernelChildMessages(context.Background(), children[0].FrameID, "user-1")
	if err != nil || len(pendingMessages) != 1 {
		t.Fatalf("pending child messages=%#v err=%v", pendingMessages, err)
	}
	if err := store.MarkKernelChildMessageConsumed(context.Background(), children[0].FrameID, "user-1", pendingMessages[0].ID, pendingMessages[0].Generation); err != nil {
		t.Fatal(err)
	}
	output := map[string]any{"response": "real child response"}
	first, err := store.CompleteKernelSupervisedChild(context.Background(), children[0].FrameID, "user-1", "completed", output, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CompleteKernelSupervisedChild(context.Background(), children[0].FrameID, "user-1", "failed", nil, "must not replace")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "completed" || second.Status != "completed" || second.Output["response"] != "real child response" {
		t.Fatalf("terminal idempotency first=%#v second=%#v", first, second)
	}
	parentEvents, err := store.ListFrameEvents("root-1", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	var notifications int
	for _, event := range parentEvents {
		if event.Type == "notification" {
			notifications++
		}
	}
	if notifications != 1 {
		t.Fatalf("notification count = %d events=%#v", notifications, parentEvents)
	}
	for index := 0; index < 51; index++ {
		artifactID := fmt.Sprintf("artifact-%02d", index)
		if _, _, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
			ArtifactID: artifactID, ProjectID: "project-1", Name: artifactID + ".txt",
			ContentType: "text/plain", Content: strings.NewReader(artifactID),
			RootFrameID: "root-1", FrameID: children[0].FrameID,
		}); err != nil {
			t.Fatalf("write artifact %d: %v", index, err)
		}
	}
	var retainedVersions []string
	for version := 1; version <= 3; version++ {
		_, stored, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
			ArtifactID: "artifact-multi", ProjectID: "project-1", Name: "multi.txt",
			ContentType: "text/plain", Content: strings.NewReader(fmt.Sprintf("version-%d", version)),
			RootFrameID: "root-1", FrameID: children[0].FrameID,
		})
		if err != nil {
			t.Fatalf("write multi version %d: %v", version, err)
		}
		retainedVersions = append(retainedVersions, stored.ID)
	}
	artifacts, omitted, err := store.ListKernelChildArtifacts(context.Background(), children[0].FrameID, "user-1")
	if err != nil || len(artifacts) != 50 || omitted != 4 {
		t.Fatalf("artifact projection len=%d omitted=%d err=%v", len(artifacts), omitted, err)
	}
	lastTwo := artifacts[len(artifacts)-2:]
	if lastTwo[0]["artifact_id"] != "artifact-multi" || lastTwo[1]["artifact_id"] != "artifact-multi" ||
		lastTwo[0]["version_id"] != retainedVersions[1] || lastTwo[1]["version_id"] != retainedVersions[2] ||
		lastTwo[1]["filename"] != "multi.txt" || lastTwo[1]["content_type"] != "text/plain" {
		t.Fatalf("artifact projection tail=%#v", lastTwo)
	}
}

func TestKernelSupervisionMessageCallIDAndCompletionGenerationAreIdempotent(t *testing.T) {
	store, _ := openNotificationTestStore(t)
	defer store.Close()
	ctx := context.Background()
	children, err := store.CreateKernelSupervisedChildren(ctx, CreateKernelDelegatesInput{
		ParentFrameID: "root-1", OwnerUserID: "owner-1", ToolUseID: "delegate-tool",
		Requests: []KernelDelegateRequest{{Task: "first pass", Name: "worker"}},
	})
	if err != nil || len(children) != 1 {
		t.Fatalf("children=%#v err=%v", children, err)
	}
	child := children[0]
	if _, err := store.CompleteKernelSupervisedChild(ctx, child.FrameID, "owner-1", "completed", map[string]any{"response": "first"}, ""); err != nil {
		t.Fatal(err)
	}
	first, err := store.QueueKernelSupervisionMessageWithID(ctx, "root-1", child.FrameID, "owner-1", "call-1", "follow up", "question")
	if err != nil || first.Queued == nil || first.Action != "resumed" {
		t.Fatalf("first queue=%#v err=%v", first, err)
	}
	retry, err := store.QueueKernelSupervisionMessageWithID(ctx, "root-1", child.FrameID, "owner-1", "call-1", "follow up", "question")
	if err != nil || retry.Queued == nil || retry.Queued.ID != first.Queued.ID || retry.Queued.Generation != first.Queued.Generation {
		t.Fatalf("retry queue=%#v err=%v", retry, err)
	}
	if _, err := store.QueueKernelSupervisionMessageWithID(ctx, "root-1", child.FrameID, "owner-1", "call-1", "changed", "question"); err == nil {
		t.Fatal("conflicting call id succeeded")
	}
	if err := store.MarkKernelChildMessageConsumed(ctx, child.FrameID, "owner-1", first.Queued.ID, first.Queued.Generation); err != nil {
		t.Fatal(err)
	}
	second, err := store.CompleteKernelSupervisedChild(ctx, child.FrameID, "owner-1", "completed", map[string]any{"response": "second"}, "")
	if err != nil || second.Status != "completed" {
		t.Fatalf("second completion=%#v err=%v", second, err)
	}
	if _, err := store.CompleteKernelSupervisedChild(ctx, child.FrameID, "owner-1", "completed", map[string]any{"response": "second"}, ""); err != nil {
		t.Fatalf("completion retry: %v", err)
	}
	var messageCount, completionCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM kernel_child_messages WHERE frame_id=?`, child.FrameID).Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE recipient_frame_id='root-1' AND notification_type='child_landed'`).Scan(&completionCount); err != nil {
		t.Fatal(err)
	}
	if messageCount != 1 || completionCount != 2 {
		t.Fatalf("message count=%d completion count=%d", messageCount, completionCount)
	}
}
