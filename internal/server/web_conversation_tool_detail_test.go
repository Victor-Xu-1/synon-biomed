package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestWebConversationToolDetailPagesNestedRecordsWithoutTruncation(t *testing.T) {
	records := make([]map[string]any, 105)
	for index := range records {
		records[index] = map[string]any{
			"id":    fmt.Sprintf("REC-%03d", index+1),
			"title": "Shared scientific title",
			"measurements": map[string]any{
				"primary": map[string]any{"value": float64(index) + 0.5, "unit": "nM"},
				"flags":   []any{"validated", fmt.Sprintf("batch-%d", index+1)},
			},
		}
	}
	raw, err := json.Marshal(map[string]any{"count": len(records), "records": records})
	if err != nil {
		t.Fatal(err)
	}

	root := requireWebConversationToolDetailPage(t, raw, "", 0, 100)
	if root.Kind != "object" || root.Total != 2 || len(root.Items) != 2 {
		t.Fatalf("root page = %#v", root)
	}
	if root.Items[1].Path != "/records" || root.Items[1].Kind != "array" || root.Items[1].Total != 105 {
		t.Fatalf("records summary = %#v", root.Items[1])
	}

	first := requireWebConversationToolDetailPage(t, raw, "/records", 0, 100)
	if first.Kind != "array" || first.Total != 105 || len(first.Items) != 100 ||
		first.Items[0].Path != "/records/0" || first.Items[99].Path != "/records/99" {
		t.Fatalf("first records page = %#v", first)
	}
	second := requireWebConversationToolDetailPage(t, raw, "/records", 100, 100)
	if second.Total != 105 || len(second.Items) != 5 || second.Items[4].Path != "/records/104" {
		t.Fatalf("second records page = %#v", second)
	}

	record := requireWebConversationToolDetailPage(t, raw, "/records/104", 0, 100)
	if record.Kind != "object" || record.Total != 3 || len(record.Items) != 3 ||
		record.Items[0].Value != "REC-105" || record.Items[1].Kind != "object" {
		t.Fatalf("record detail = %#v", record)
	}
	measurements := requireWebConversationToolDetailPage(t, raw, "/records/104/measurements/primary", 0, 100)
	var measurementValue any
	for _, item := range measurements.Items {
		if item.Key == "value" {
			measurementValue = item.Value
		}
	}
	if measurements.Kind != "object" || measurements.Total != 2 || measurementValue != json.Number("104.5") {
		t.Fatalf("measurement detail = %#v", measurements)
	}
}

func TestWebConversationToolDetailCursorBindsMessageSectionPathAndRevision(t *testing.T) {
	cursor, err := encodeWebConversationToolDetailCursor(webConversationToolDetailCursor{
		Version: 1, MessageID: "message-1", BranchID: "br_deadbeef",
		Section: "output", Path: "/records", Revision: 7,
		Offset: 100, ByteOffset: 4096, Total: 105, Kind: "array",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/?cursor="+cursor, nil)
	requestedBranch, status, err := webConversationToolDetailRequestedBranch(request)
	if err != nil || status != http.StatusOK || requestedBranch != "br_deadbeef" {
		t.Fatalf("requested branch=%q status=%d err=%v", requestedBranch, status, err)
	}
	parsed, status, err := parseWebConversationToolDetailRequest(
		request, "message-1", map[string]any{"revision": float64(7)}, "br_deadbeef",
	)
	if err != nil || status != http.StatusOK || parsed.Section != "output" || parsed.Path != "/records" ||
		parsed.Offset != 100 || parsed.Revision != 7 || parsed.Cursor == nil || parsed.Cursor.ByteOffset != 4096 {
		t.Fatalf("parsed cursor=%#v status=%d err=%v", parsed, status, err)
	}

	conflict := httptest.NewRequest(http.MethodGet, "/?cursor="+cursor+"&path=/items", nil)
	_, status, err = parseWebConversationToolDetailRequest(
		conflict, "message-1", map[string]any{"revision": float64(7)}, "br_deadbeef",
	)
	if err == nil || status != http.StatusBadRequest {
		t.Fatalf("conflicting cursor status=%d err=%v", status, err)
	}
	branchConflict := httptest.NewRequest(http.MethodGet, "/?cursor="+cursor+"&branch_id=br_cafebabe", nil)
	if _, status, err := webConversationToolDetailRequestedBranch(branchConflict); err == nil || status != http.StatusBadRequest {
		t.Fatalf("conflicting branch cursor status=%d err=%v", status, err)
	}

	stale := httptest.NewRequest(http.MethodGet, "/?cursor="+cursor, nil)
	_, status, err = parseWebConversationToolDetailRequest(
		stale, "message-1", map[string]any{"revision": float64(8)}, "br_deadbeef",
	)
	if err == nil || status != http.StatusBadRequest {
		t.Fatalf("stale cursor status=%d err=%v", status, err)
	}
}

func TestWebConversationToolDetailRejectsUnsafePathsAndDescriptors(t *testing.T) {
	for _, path := range []string{"records", "/bad~2escape", "/bad~", "/" + strings.Repeat("x", 129)} {
		if _, err := parseWebConversationToolDetailPath(path); err == nil {
			t.Fatalf("path %q unexpectedly accepted", path)
		}
	}
	for path, expected := range map[string][]string{
		"/":      {""},
		"/a~1b":  {"a/b"},
		"/a~01b": {"a~1b"},
	} {
		if segments, err := parseWebConversationToolDetailPath(path); err != nil || !reflect.DeepEqual(segments, expected) {
			t.Fatalf("path=%q segments=%#v expected=%#v err=%v", path, segments, expected, err)
		}
	}
	if _, ok := decodeWebConversationLargeToolResultDescriptor(`{"truncated":true,"artifact_id":"large-tool-result-abc","version_id":"ltr-bad","content_url":"/api/artifacts/large-tool-result-abc/versions/ltr-bad"}`); ok {
		t.Fatal("invalid large-result descriptor accepted")
	}
	if _, err := readWebConversationToolDetailPage(strings.NewReader(`{"duplicate":1,"duplicate":2}`), nil, "", 0, 100); err == nil {
		t.Fatal("duplicate object keys were accepted")
	}
	for _, raw := range []string{
		`{"` + strings.Repeat("x", maxWebConversationToolDetailKeyBytes+1) + `":1}`,
		"{\"unsafe\\u0000key\":1}",
	} {
		if _, err := readWebConversationToolDetailPage(strings.NewReader(raw), nil, "", 0, 100); err == nil {
			t.Fatalf("unsafe object key was accepted: %q", raw)
		}
	}
}

func TestWebConversationToolDetailReadsBoundLargeResultWithOwnerIsolation(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-large-detail", "frame-large-detail")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-large-detail", MessageUUID: "user-large-detail", ClientMessageID: "user-client", Text: "inspect",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-large-detail")
	if err != nil || !found {
		t.Fatalf("stream found=%t err=%v", found, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-large-detail", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	appendRunnerToolCheckpoint(t, repo, claim.Claim, "large-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-large-detail", "toolName": "Read",
		"toolInput": map[string]any{"path": "records.json"},
	})
	records := make([]any, 105)
	for index := range records {
		records[index] = map[string]any{"id": fmt.Sprintf("LARGE-%03d", index+1), "title": "Shared title"}
	}
	raw, err := json.Marshal(map[string]any{"records": records})
	if err != nil {
		t.Fatal(err)
	}
	large, err := store.WriteRunnerLargeToolResult(context.Background(), workspace.WriteRunnerLargeToolResultInput{
		ArtifactID: "large-tool-result-0123456789abcdef0123456789abcdef",
		ProjectID:  "project-large-detail", RootFrameID: "frame-large-detail", FrameID: "frame-large-detail",
		StreamUID: stream.UID, OwnerUserID: "local", RunnerID: "runner-large-detail", ClaimToken: claim.Claim.ClaimToken,
		Attempt: claim.Claim.Attempt, SourceEventID: 1, ToolName: "Read", ToolCallID: "call-large-detail", Content: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := map[string]any{
		"artifact_id": large.ArtifactID, "version_id": large.VersionID,
		"content_url": "/api/artifacts/" + url.PathEscape(large.ArtifactID) + "/versions/" + url.PathEscape(large.VersionID),
		"truncated":   true,
	}
	appendRunnerToolCheckpoint(t, repo, claim.Claim, "large-complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "call-large-detail", "toolName": "Read",
		"toolInput": map[string]any{"path": "records.json"}, "toolResult": descriptor,
	})
	page := transcriptToolHistoryPage(t, server, "/api/conversations/frame-large-detail/messages?limit=10", "")
	rawMessages, _ := page["items"].([]any)
	messages := make([]map[string]any, 0, len(rawMessages))
	for _, rawMessage := range rawMessages {
		if message, ok := rawMessage.(map[string]any); ok {
			messages = append(messages, message)
		}
	}
	toolMessage := transcriptToolMessageByCallID(t, messages, "call-large-detail")
	messageID := webString(toolMessage["id"])
	response := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-large-detail/messages/"+url.PathEscape(messageID)+
			"/tool-detail?section=output&path="+url.QueryEscape("/records")+"&limit=100", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("large detail status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("large detail security headers=%#v", response.Header())
	}
	payload := p3DecodeObject(t, response)
	items, _ := payload["items"].([]any)
	nextCursor := webString(payload["next_cursor"])
	if payload["branch_id"] == "" || payload["total"] != float64(105) || len(items) != 100 || nextCursor == "" {
		t.Fatalf("large detail payload=%#v", payload)
	}
	second := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-large-detail/messages/"+url.PathEscape(messageID)+
			"/tool-detail?cursor="+url.QueryEscape(nextCursor), nil, "")
	if second.Code != http.StatusOK {
		t.Fatalf("second large detail status=%d body=%s", second.Code, second.Body.String())
	}
	secondPayload := p3DecodeObject(t, second)
	secondItems, _ := secondPayload["items"].([]any)
	if secondPayload["from"] != float64(100) || len(secondItems) != 5 || secondPayload["next_cursor"] != nil {
		t.Fatalf("second large detail payload=%#v", secondPayload)
	}
	foreign := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-large-detail/messages/"+url.PathEscape(messageID)+"/tool-detail", nil, "foreign")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign large detail status=%d body=%s", foreign.Code, foreign.Body.String())
	}
}

func TestWebConversationToolDetailCursorContinuesWithoutRescanningLargePrefix(t *testing.T) {
	records := make([]any, 50_000)
	for index := range records {
		records[index] = map[string]any{"id": fmt.Sprintf("REC-%05d", index), "score": index}
	}
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	firstReader := &countingToolDetailReadSeeker{Reader: bytes.NewReader(raw)}
	first, err := readWebConversationToolDetailPage(firstReader, nil, "", 0, 100)
	if err != nil || first.Total != 50_000 || len(first.Items) != 100 || first.NextByteOffset <= 0 {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	continuedReader := &countingToolDetailReadSeeker{Reader: bytes.NewReader(raw)}
	second, err := readWebConversationToolDetailContinuation(continuedReader, "", webConversationToolDetailCursor{
		Version: 1, MessageID: "message", BranchID: "br_deadbeef", Section: "output", Revision: 1,
		Offset: 100, ByteOffset: first.NextByteOffset, Total: first.Total, Kind: first.Kind,
	}, 100)
	if err != nil || second.Total != 50_000 || len(second.Items) != 100 || second.Items[0].Index == nil ||
		*second.Items[0].Index != 100 {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	if continuedReader.ReadBytes >= int64(len(raw))/4 {
		t.Fatalf("continuation reread %d of %d bytes", continuedReader.ReadBytes, len(raw))
	}
}

func TestWebConversationToolDetailRejectsExcessiveNestingAndHonorsCancellation(t *testing.T) {
	deep := strings.Repeat("[", maxWebConversationToolDetailDepth+3) + "0" +
		strings.Repeat("]", maxWebConversationToolDetailDepth+3)
	if _, err := readWebConversationToolDetailPage(strings.NewReader(deep), nil, "", 0, 100); err == nil {
		t.Fatal("excessively nested tool detail was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := &webConversationToolDetailContextReader{
		Context: ctx, ReadSeeker: bytes.NewReader([]byte(`{"records":[1,2,3]}`)),
	}
	if _, err := readWebConversationToolDetailPage(reader, nil, "", 0, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read error=%v", err)
	}
}

func TestWebConversationToolDetailBoundsLargeScalarResponsesWithoutSilentLoss(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"sequence": strings.Repeat("生\u0001", maxWebConversationToolDetailScalarBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	page := requireWebConversationToolDetailPage(t, raw, "", 0, 100)
	if len(page.Items) != 1 || !page.Items[0].Truncated ||
		page.Items[0].OriginalBytes <= maxWebConversationToolDetailScalarBytes {
		t.Fatalf("large scalar node=%#v", page.Items)
	}
	encoded, err := json.Marshal(page)
	if err != nil || len(encoded) > 128<<10 {
		t.Fatalf("bounded scalar response bytes=%d err=%v", len(encoded), err)
	}
	selected := requireWebConversationToolDetailPage(t, raw, "/sequence", 0, 100)
	if len(selected.Items) != 1 || !selected.Items[0].Truncated || selected.Items[0].Value == nil {
		t.Fatalf("selected scalar page=%#v", selected)
	}
	unsafeNumbers := requireWebConversationToolDetailPage(
		t, []byte(`{"large_integer":9007199254740992,"overflow":1e10000}`), "", 0, 100,
	)
	if unsafeNumbers.Items[0].Value != "9007199254740992" || unsafeNumbers.Items[1].Value != "1e10000" {
		t.Fatalf("unsafe JSON numbers were not preserved exactly: %#v", unsafeNumbers.Items)
	}
}

type countingToolDetailReadSeeker struct {
	*bytes.Reader
	ReadBytes int64
}

func (reader *countingToolDetailReadSeeker) Read(buffer []byte) (int, error) {
	count, err := reader.Reader.Read(buffer)
	reader.ReadBytes += int64(count)
	return count, err
}

func requireWebConversationToolDetailPage(
	t *testing.T,
	raw []byte,
	path string,
	offset, limit int,
) webConversationToolDetailPage {
	t.Helper()
	segments, err := parseWebConversationToolDetailPath(path)
	if err != nil {
		t.Fatal(err)
	}
	page, err := readWebConversationToolDetailPage(strings.NewReader(string(raw)), segments, path, offset, limit)
	if err != nil {
		t.Fatal(err)
	}
	return page
}
