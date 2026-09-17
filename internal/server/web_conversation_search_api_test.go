package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestWebConversationSearchUsesVisibleTranscriptProjectionAndTitleFallback(t *testing.T) {
	store, repository, database := newTranscriptWebFixture(t)
	readModel := transcriptstore.NewWebReadModelRepository(database, database)
	server := &Server{
		workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel,
	}
	seedSearchServerConversation(t, server, store, repository, readModel,
		"owner-search", "project-search", "frame-message", "Evidence review",
		"rare transcript needle from the active message")
	seedSearchServerConversation(t, server, store, repository, readModel,
		"foreign-owner", "project-foreign", "frame-foreign", "Foreign review",
		"rare transcript needle owned by somebody else")
	seedTranscriptWebFrame(t, store, "owner-search", "project-title", "frame-title")
	title := "Rare title-only conversation"
	if _, err := store.UpdateFrame("frame-title", workspace.UpdateFrameInput{Name: &title}); err != nil {
		t.Fatal(err)
	}

	messageSearch := executeWebConversationSearch(t, server, "owner-search", "rare transcript needle")
	if messageSearch.Code != http.StatusOK {
		t.Fatalf("message search status=%d body=%s", messageSearch.Code, messageSearch.Body.String())
	}
	messagePayload := p3DecodeObject(t, messageSearch)
	messageItems, _ := messagePayload["items"].([]any)
	if len(messageItems) != 1 {
		t.Fatalf("message search payload=%#v", messagePayload)
	}
	messageItem, _ := messageItems[0].(map[string]any)
	conversation, _ := messageItem["conversation"].(map[string]any)
	if webString(conversation["id"]) != "frame-message" || webString(messageItem["message_id"]) == "" ||
		webString(messageItem["match_kind"]) != "message" || webString(messageItem["project_name"]) != "project-search" {
		t.Fatalf("message search item=%#v", messageItem)
	}
	if messageItem["preview_text"] != "rare transcript needle from the active message" {
		t.Fatalf("message preview=%#v", messageItem["preview_text"])
	}

	titleSearch := executeWebConversationSearch(t, server, "owner-search", "title-only")
	titlePayload := p3DecodeObject(t, titleSearch)
	titleItems, _ := titlePayload["items"].([]any)
	if titleSearch.Code != http.StatusOK || len(titleItems) != 1 {
		t.Fatalf("title search status=%d payload=%#v", titleSearch.Code, titlePayload)
	}
	titleItem, _ := titleItems[0].(map[string]any)
	titleConversation, _ := titleItem["conversation"].(map[string]any)
	if webString(titleConversation["id"]) != "frame-title" || webString(titleItem["message_id"]) != "" ||
		webString(titleItem["match_kind"]) != "title" {
		t.Fatalf("title search item=%#v", titleItem)
	}

	recent := executeWebConversationSearch(t, server, "owner-search", "")
	recentPayload := p3DecodeObject(t, recent)
	recentItems, _ := recentPayload["items"].([]any)
	if recent.Code != http.StatusOK || len(recentItems) != 2 {
		t.Fatalf("recent status=%d payload=%#v", recent.Code, recentPayload)
	}
}

func TestWebConversationSearchRejectsInvalidContract(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(http.MethodPost, "/api/conversations/search", nil)
	response := httptest.NewRecorder()
	server.handleWebConversationSearch(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status=%d body=%s", response.Code, response.Body.String())
	}
}

func seedSearchServerConversation(
	t *testing.T,
	server *Server,
	store *workspace.Store,
	repository *transcriptstore.Repository,
	readModel *transcriptstore.WebReadModelRepository,
	ownerID string,
	projectID string,
	frameID string,
	name string,
	message string,
) {
	t.Helper()
	seedTranscriptWebFrame(t, store, ownerID, projectID, frameID)
	if _, err := store.UpdateFrame(frameID, workspace.UpdateFrameInput{Name: &name}); err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-" + frameID, OwnerID: ownerID, ExternalID: frameID, SessionID: frameID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: projectID, RootFrameID: frameID,
		FrameID: frameID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := repository.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: ownerID, ClientMessageID: "message-" + frameID,
		PayloadJSON:  []byte(`{"role":"user","text":` + strconv.Quote(message) + `}`),
		Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append message created=%t err=%v", created, err)
	}
	work := requireSingleTranscriptWebProjectionWork(t, readModel, ownerID)
	if err := server.rebuildTranscriptWebReadModel(context.Background(), work); err != nil {
		t.Fatal(err)
	}
}

func executeWebConversationSearch(
	t *testing.T,
	server *Server,
	ownerID string,
	query string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/conversations/search?q="+url.QueryEscape(query)+"&page=0&page_size=20",
		nil,
	)
	request.Header.Set("X-Synon-User-Id", ownerID)
	response := httptest.NewRecorder()
	server.handleWebConversation(response, request)
	return response
}
