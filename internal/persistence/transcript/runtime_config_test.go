package transcript

import (
	"context"
	"testing"
)

func TestLatestFrameRuntimeConfigMergesPartialInputPatchesAndHonorsExplicitOverride(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-runtime-config','owner-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES('frame-runtime-config','project-runtime-config','frame-runtime-config')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "stream-runtime-config", OwnerID: "owner-a", ExternalID: "frame-runtime-config",
		SessionID: "frame-runtime-config", Kind: StreamKindFrameRef, ProjectID: "project-runtime-config",
		RootFrameID: "frame-runtime-config", FrameID: "frame-runtime-config", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	appendInput := func(clientID string, runtimeConfig map[string]any) {
		t.Helper()
		if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
			StreamUID: "stream-runtime-config", OwnerID: "owner-a", ClientMessageID: clientID,
			FrameEventID: "frame-event-" + clientID, MessageUUID: "message-" + clientID,
			Text: clientID, RuntimeConfig: runtimeConfig,
		}); err != nil || !created {
			t.Fatalf("append %s created=%t err=%v", clientID, created, err)
		}
	}

	appendInput("initial", map[string]any{
		"agentName": "OPERON", "model": "mimo-v2.5", "memoryMode": "on", "verifierMode": "on",
	})
	appendInput("continue", map[string]any{"agentName": "OPERON"})
	config, found, err := repo.LatestFrameRuntimeConfig(context.Background(), "stream-runtime-config", "owner-a")
	if err != nil || !found {
		t.Fatalf("merged config found=%t err=%v", found, err)
	}
	if config["agentName"] != "OPERON" || config["model"] != "mimo-v2.5" ||
		config["memoryMode"] != "on" || config["verifierMode"] != "on" {
		t.Fatalf("partial continuation erased durable config: %#v", config)
	}

	appendInput("disable-reviewer", map[string]any{"agentName": "OPERON", "verifierMode": "off"})
	config, found, err = repo.LatestFrameRuntimeConfig(context.Background(), "stream-runtime-config", "owner-a")
	if err != nil || !found || config["verifierMode"] != "off" || config["memoryMode"] != "on" {
		t.Fatalf("explicit override config=%#v found=%t err=%v", config, found, err)
	}
}
