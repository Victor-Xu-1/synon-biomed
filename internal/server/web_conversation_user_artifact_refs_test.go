package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestWebConversationUserArtifactReferencesAreCanonicalAndOwnerScoped(t *testing.T) {
	app, store := newUserArtifactReferenceServer(t)
	project := createP3Project(t, store, "project-attachments", "local")
	conversationID := createArtifactReferenceConversation(t, app, store, project)

	profile, profileVersion, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "artifact-profile", ProjectID: project.ID, Name: "onboarding-profile.md",
		ContentType: "text/markdown", Content: strings.NewReader("# Onboarding profile\n\nResearcher"),
		CreatedBy: "local", IsUserUpload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	other, otherVersion, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "artifact-other", ProjectID: project.ID, Name: "other.txt",
		ContentType: "text/plain", Content: strings.NewReader("other"), CreatedBy: "local", IsUserUpload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	foreignProject := createP3Project(t, store, "project-foreign-attachments", "foreign")
	foreign, foreignVersion, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "artifact-foreign", ProjectID: foreignProject.ID, Name: "private.txt",
		ContentType: "text/plain", Content: strings.NewReader("private"), CreatedBy: "foreign", IsUserUpload: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		refs []map[string]any
		want int
	}{
		{name: "foreign owner", refs: []map[string]any{{"artifact_id": foreign.ID, "version_id": foreignVersion.ID}}, want: http.StatusNotFound},
		{name: "mismatched exact pair", refs: []map[string]any{{"artifact_id": other.ID, "version_id": profileVersion.ID}}, want: http.StatusNotFound},
		{name: "missing version", refs: []map[string]any{{"artifact_id": profile.ID, "version_id": "missing-version"}}, want: http.StatusNotFound},
		{name: "duplicate pair", refs: []map[string]any{{"artifact_id": profile.ID, "version_id": profileVersion.ID}, {"artifact_id": profile.ID, "version_id": profileVersion.ID}}, want: http.StatusBadRequest},
		{name: "same owner wrong artifact", refs: []map[string]any{{"artifact_id": profile.ID, "version_id": otherVersion.ID}}, want: http.StatusNotFound},
		{name: "wrong profile filename", refs: []map[string]any{{"artifact_id": other.ID, "version_id": otherVersion.ID}}, want: http.StatusBadRequest},
		{name: "missing profile artifact", refs: []map[string]any{}, want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
				"content": "Analyze the cohort", "files": []string{}, "artifact_refs": test.refs,
				"message_context": "onboarding_first_task", "loading_id": "reject-" + strings.ReplaceAll(test.name, " ", "-"),
				"inject_skills": []string{}, "session_options": map[string]any{},
			}, "local")
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}

	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, found, err := repository.GetFrameStreamBySession(context.Background(), "local", conversationID)
	if err != nil || !found {
		t.Fatalf("stream found=%v err=%v", found, err)
	}
	before, err := repository.ListRunnerReplay(context.Background(), transcriptstore.ListRunnerReplayInput{
		StreamUID: stream.UID, OwnerID: "local", MessageLimit: 20, CheckpointLimit: 20,
	})
	if err != nil || len(before) != 0 {
		t.Fatalf("rejected requests mutated transcript: len=%d err=%v", len(before), err)
	}

	accepted := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Analyze the cohort", "files": []string{"cohort.csv"},
		"artifact_refs": []map[string]any{
			{"artifact_id": profile.ID, "version_id": profileVersion.ID},
			{"artifact_id": other.ID, "version_id": otherVersion.ID},
		},
		"message_context": "onboarding_first_task", "loading_id": "onboarding-first-task",
		"inject_skills": []string{}, "session_options": map[string]any{},
	}, "local")
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("accepted status=%d body=%s", accepted.Code, accepted.Body.String())
	}
	acceptedID := webString(p3DecodeObject(t, accepted)["msg_id"])
	repeated := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Analyze the cohort", "files": []string{"cohort.csv"},
		"artifact_refs": []map[string]any{
			{"artifact_id": profile.ID, "version_id": profileVersion.ID},
			{"artifact_id": other.ID, "version_id": otherVersion.ID},
		},
		"message_context": "onboarding_first_task", "loading_id": "onboarding-first-task",
		"inject_skills": []string{}, "session_options": map[string]any{},
	}, "local")
	if repeated.Code != http.StatusAccepted || webString(p3DecodeObject(t, repeated)["msg_id"]) != acceptedID {
		t.Fatalf("repeated status=%d body=%s acceptedID=%q", repeated.Code, repeated.Body.String(), acceptedID)
	}
	conflictingRetry := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Analyze the cohort", "files": []string{},
		"artifact_refs":   []map[string]any{{"artifact_id": profile.ID, "version_id": profileVersion.ID}},
		"message_context": "onboarding_first_task", "loading_id": "onboarding-first-task",
		"inject_skills": []string{}, "session_options": map[string]any{},
	}, "local")
	if conflictingRetry.Code != http.StatusConflict {
		t.Fatalf("conflicting retry status=%d body=%s", conflictingRetry.Code, conflictingRetry.Body.String())
	}
	inputConflict := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Analyze the cohort", "files": []string{"different.csv"},
		"artifact_refs": []map[string]any{
			{"artifact_id": profile.ID, "version_id": profileVersion.ID},
			{"artifact_id": other.ID, "version_id": otherVersion.ID},
		},
		"message_context": "onboarding_first_task", "loading_id": "onboarding-first-task",
		"inject_skills": []string{}, "session_options": map[string]any{},
	}, "local")
	if inputConflict.Code != http.StatusConflict {
		t.Fatalf("input conflict status=%d body=%s", inputConflict.Code, inputConflict.Body.String())
	}
	secondConversationID := createArtifactReferenceConversation(t, app, store, project)
	secondConversation := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+secondConversationID+"/messages", map[string]any{
		"content": "Analyze the cohort", "files": []string{"cohort.csv"},
		"artifact_refs": []map[string]any{
			{"artifact_id": profile.ID, "version_id": profileVersion.ID},
			{"artifact_id": other.ID, "version_id": otherVersion.ID},
		},
		"message_context": "onboarding_first_task", "loading_id": "onboarding-first-task",
		"inject_skills": []string{}, "session_options": map[string]any{},
	}, "local")
	if secondConversation.Code != http.StatusAccepted ||
		webString(p3DecodeObject(t, secondConversation)["msg_id"]) == acceptedID {
		t.Fatalf("second conversation status=%d body=%s firstID=%q", secondConversation.Code, secondConversation.Body.String(), acceptedID)
	}

	replay, err := repository.ListRunnerReplay(context.Background(), transcriptstore.ListRunnerReplayInput{
		StreamUID: stream.UID, OwnerID: "local", MessageLimit: 20, CheckpointLimit: 20,
	})
	if err != nil || len(replay) != 1 {
		t.Fatalf("replay len=%d err=%v", len(replay), err)
	}
	var payload map[string]any
	if err := json.Unmarshal(replay[0].ResolvedPayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	refs, err := decodeUserArtifactReferences(payload["artifactRefs"])
	if err != nil || len(refs) != 2 {
		t.Fatalf("refs=%#v err=%v payload=%#v", refs, err, payload)
	}
	if refs[0].ArtifactID != profile.ID || refs[0].VersionID != profileVersion.ID ||
		refs[0].Filename != "onboarding-profile.md" || refs[0].ContentType != "text/markdown" ||
		refs[0].Checksum != profileVersion.ContentSHA256 {
		t.Fatalf("canonical reference mismatch: %#v", refs[0])
	}
	if refs[1].ArtifactID != other.ID || refs[1].VersionID != otherVersion.ID || refs[1].Filename != "other.txt" {
		t.Fatalf("uploaded reference mismatch: %#v", refs[1])
	}
	if _, err := store.RenameArtifact(profile.ID, "renamed-profile.md"); err != nil {
		t.Fatal(err)
	}
	retryInput := transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: "local", ClientMessageID: replay[0].Event.ClientMessageID,
		FrameEventID: "idempotent-retry", MessageUUID: webString(payload["messageUuid"]), Text: "Analyze the cohort",
		ArtifactReferences: []transcriptstore.UserArtifactReferenceInput{
			{ArtifactID: profile.ID, VersionID: profileVersion.ID},
			{ArtifactID: other.ID, VersionID: otherVersion.ID},
		},
		MessageContext: "onboarding_first_task", RuntimeConfig: payload["runtimeConfig"].(map[string]any),
		InputData:    map[string]any{"request": "Analyze the cohort", "files": []any{"cohort.csv"}},
		Destinations: []string{transcriptWebDestination},
	}
	if repeated, _, created, err := repository.AppendFrameUserEvent(context.Background(), retryInput); err != nil || created || repeated.EventID != replay[0].Event.EventID {
		t.Fatalf("idempotent retry event=%#v created=%v err=%v", repeated, created, err)
	}
	conflict := retryInput
	conflict.ArtifactReferences = []transcriptstore.UserArtifactReferenceInput{{ArtifactID: profile.ID, VersionID: profileVersion.ID}}
	if _, _, _, err := repository.AppendFrameUserEvent(context.Background(), conflict); err == nil {
		t.Fatal("same client identity accepted a different artifact version")
	}
	inputDataConflict := retryInput
	inputDataConflict.InputData = map[string]any{
		"request": "Analyze the cohort", "files": []any{"cohort.csv"}, "inject_skills": []any{"other-skill"},
	}
	if _, _, _, err := repository.AppendFrameUserEvent(context.Background(), inputDataConflict); err == nil {
		t.Fatal("same client identity accepted different semantic input data")
	}

	authority := &transcriptRunnerAuthority{Stream: stream, Claim: transcriptstore.RunnerClaim{RunnerID: "runner-model-context"}}
	entries, err := app.loadTranscriptRunnerReplay(context.Background(), authority, 20, 20)
	if err != nil {
		t.Fatalf("load real runner replay: %v", err)
	}
	messages := sessionEntriesToChatMessages("system", entries)
	if len(messages) != 2 {
		t.Fatalf("model messages=%#v", messages)
	}
	wantPrefix := "[System] Onboarding complete — first task chosen during setup.\n\nAnalyze the cohort\n\n"
	if !strings.HasPrefix(messages[1].Content, wantPrefix) ||
		!strings.Contains(messages[1].Content, `"artifact_ref":"{{artifact:`+profileVersion.ID+`}}"`) ||
		!strings.Contains(messages[1].Content, `"filename":"onboarding-profile.md"`) ||
		!strings.Contains(messages[1].Content, `"artifact_ref":"{{artifact:`+otherVersion.ID+`}}"`) ||
		!strings.Contains(messages[1].Content, `"filename":"other.txt"`) {
		t.Fatalf("model content is not reference aligned: %q", messages[1].Content)
	}

	history := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+conversationID+"/messages?limit=20", nil, "local")
	if history.Code != http.StatusOK {
		t.Fatalf("history status=%d body=%s", history.Code, history.Body.String())
	}
	items, _ := p3DecodeObject(t, history)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("history items=%#v", items)
	}
	message, _ := items[0].(map[string]any)
	content, _ := message["content"].(map[string]any)
	if webString(content["content"]) != "Analyze the cohort" || strings.Contains(webString(content["content"]), "Onboarding complete") {
		t.Fatalf("public text leaked model context: %#v", message)
	}
	historyRefs, _ := message["artifact_refs"].([]any)
	if len(historyRefs) != 2 {
		t.Fatalf("history refs=%#v", message["artifact_refs"])
	}
	historyRef, _ := historyRefs[0].(map[string]any)
	if webString(historyRef["artifact_id"]) != profile.ID || webString(historyRef["version_id"]) != profileVersion.ID ||
		webString(historyRef["filename"]) != "onboarding-profile.md" || webString(historyRef["relation"]) != "attached" {
		t.Fatalf("history reference changed after artifact rename: %#v", historyRef)
	}
	uploadedHistoryRef, _ := historyRefs[1].(map[string]any)
	if webString(uploadedHistoryRef["artifact_id"]) != other.ID ||
		webString(uploadedHistoryRef["version_id"]) != otherVersion.ID || webString(uploadedHistoryRef["filename"]) != "other.txt" {
		t.Fatalf("history uploaded reference mismatch: %#v", uploadedHistoryRef)
	}
}

func TestWebConversationQueuedArtifactReferenceSurvivesDurableDelivery(t *testing.T) {
	app, store := newUserArtifactReferenceServer(t)
	project := createP3Project(t, store, "project-queued-attachments", "local")
	conversationID := createArtifactReferenceConversation(t, app, store, project)
	profile, version, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "artifact-queued-profile", ProjectID: project.ID, Name: "onboarding-profile.md",
		ContentType: "text/markdown", Content: strings.NewReader("# Onboarding profile"), CreatedBy: "local", IsUserUpload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	processing := "processing"
	if _, err := store.UpdateFrame(conversationID, workspace.UpdateFrameInput{Status: &processing}); err != nil {
		t.Fatal(err)
	}
	response := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Analyze after the current turn", "files": []string{},
		"artifact_refs":   []map[string]any{{"artifact_id": profile.ID, "version_id": version.ID}},
		"message_context": "onboarding_first_task", "loading_id": "queued-onboarding-task",
		"inject_skills": []string{}, "session_options": map[string]any{},
	}, "local")
	if response.Code != http.StatusAccepted {
		t.Fatalf("queue status=%d body=%s", response.Code, response.Body.String())
	}
	messageID := webString(p3DecodeObject(t, response)["msg_id"])
	repeated := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Analyze after the current turn", "files": []string{},
		"artifact_refs":   []map[string]any{{"artifact_id": profile.ID, "version_id": version.ID}},
		"message_context": "onboarding_first_task", "loading_id": "queued-onboarding-task",
		"inject_skills": []string{}, "session_options": map[string]any{},
	}, "local")
	if repeated.Code != http.StatusAccepted || webString(p3DecodeObject(t, repeated)["msg_id"]) != messageID {
		t.Fatalf("repeat queue status=%d body=%s messageID=%q", repeated.Code, repeated.Body.String(), messageID)
	}
	conflict := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Changed queued request", "files": []string{},
		"artifact_refs":   []map[string]any{{"artifact_id": profile.ID, "version_id": version.ID}},
		"message_context": "onboarding_first_task", "loading_id": "queued-onboarding-task",
		"inject_skills": []string{}, "session_options": map[string]any{},
	}, "local")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed queue status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	runtimeConflict := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Analyze after the current turn", "files": []string{},
		"artifact_refs":   []map[string]any{{"artifact_id": profile.ID, "version_id": version.ID}},
		"message_context": "onboarding_first_task", "loading_id": "queued-onboarding-task",
		"inject_skills": []string{}, "session_options": map[string]any{"model": "different-model"},
	}, "local")
	if runtimeConflict.Code != http.StatusConflict {
		t.Fatalf("changed runtime status=%d body=%s", runtimeConflict.Code, runtimeConflict.Body.String())
	}
	intent, found, err := store.GetCompatibilityMessageIntent(messageID)
	if err != nil || !found {
		t.Fatalf("intent found=%v err=%v", found, err)
	}
	queuedRefs, queuedContext, err := compatibilityQueuedArtifactContext(intent.DeliveryPayload)
	if err != nil || queuedContext != "onboarding_first_task" || len(queuedRefs) != 1 ||
		queuedRefs[0].ArtifactID != profile.ID || queuedRefs[0].VersionID != version.ID {
		t.Fatalf("queued refs=%#v context=%q err=%v", queuedRefs, queuedContext, err)
	}
	advanced, err := app.advanceCompatibilityFrameAfterRunner(conversationID, "completed")
	if err != nil || advanced != 1 {
		t.Fatalf("advanced=%d err=%v", advanced, err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, found, err := repository.GetFrameStreamBySession(context.Background(), "local", conversationID)
	if err != nil || !found {
		t.Fatalf("stream found=%v err=%v", found, err)
	}
	replay, err := repository.ListRunnerReplay(context.Background(), transcriptstore.ListRunnerReplayInput{
		StreamUID: stream.UID, OwnerID: "local", MessageLimit: 20, CheckpointLimit: 20,
	})
	if err != nil || len(replay) != 1 {
		t.Fatalf("replay len=%d err=%v", len(replay), err)
	}
	var payload map[string]any
	if err := json.Unmarshal(replay[0].ResolvedPayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	refs, err := decodeUserArtifactReferences(payload["artifactRefs"])
	if err != nil || len(refs) != 1 || refs[0].VersionID != version.ID {
		t.Fatalf("delivered refs=%#v err=%v", refs, err)
	}
}

func newUserArtifactReferenceServer(t *testing.T) (*Server, *workspace.Store) {
	t.Helper()
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app := newV11TestServer(t, Options{Workspace: store, Transcript: repository, FileRoot: t.TempDir()})
	// Conversation creation schedules MCP catalog work even without the
	// background-service option. Join it before the caller-owned workspace.
	t.Cleanup(func() { closeTestServer(t, app) })
	return app, store
}

func createArtifactReferenceConversation(
	t *testing.T,
	app *Server,
	store *workspace.Store,
	project workspace.CompatibilityProject,
) string {
	t.Helper()
	response := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name": "Artifact analysis", "assistant": map[string]any{"id": "synonbiomed:OPERON"},
		"extra": map[string]any{"project_id": project.ID, "project_name": project.Name},
	}, "local")
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	conversationID := webString(p3DecodeObject(t, response)["id"])
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + conversationID, OwnerID: "local", ExternalID: conversationID, SessionID: conversationID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: project.ID,
		RootFrameID: conversationID, FrameID: conversationID, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	return conversationID
}
