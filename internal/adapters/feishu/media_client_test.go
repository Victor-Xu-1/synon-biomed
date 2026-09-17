package feishu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMediaClientUploadsImageWithMultipartOpenAPIShape(t *testing.T) {
	var seenAuth string
	var seenImageType string
	var seenFilePayload string

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open-apis/im/v1/images" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		seenAuth = r.Header.Get("Authorization")
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("multipart reader: %v", err)
		}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("next multipart part: %v", err)
			}
			data, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("read part: %v", err)
			}
			switch part.FormName() {
			case "image_type":
				seenImageType = string(data)
			case "image":
				seenFilePayload = string(data)
				if part.FileName() == "" {
					t.Fatal("image part missing filename")
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"image_key":"img_real_123"}}`))
	}))
	defer api.Close()

	client := NewMediaClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	key, err := client.UploadImage(context.Background(), []byte("PNGDATA"), "image/png")
	if err != nil {
		t.Fatalf("UploadImage() error = %v", err)
	}
	if key != "img_real_123" {
		t.Fatalf("image key = %q", key)
	}
	if seenAuth != "Bearer tenant-token" || seenImageType != "message" || seenFilePayload != "PNGDATA" {
		t.Fatalf("auth=%q image_type=%q payload=%q", seenAuth, seenImageType, seenFilePayload)
	}
}

func TestMediaClientUploadsFileWithMappedFileTypeAndSendsMessages(t *testing.T) {
	var mu sync.Mutex
	seen := make([]map[string]any, 0)
	var fileType string
	var fileName string
	var filePayload string

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/open-apis/im/v1/files":
			if r.Method != http.MethodPost {
				t.Fatalf("file method = %s", r.Method)
			}
			reader, err := r.MultipartReader()
			if err != nil {
				t.Fatalf("multipart reader: %v", err)
			}
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("next multipart part: %v", err)
				}
				data, err := io.ReadAll(part)
				if err != nil {
					t.Fatalf("read part: %v", err)
				}
				switch part.FormName() {
				case "file_type":
					fileType = string(data)
				case "file_name":
					fileName = string(data)
				case "file":
					filePayload = string(data)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"file_key":"file_real_456"}}`))
		case "/open-apis/im/v1/messages":
			if r.URL.Query().Get("receive_id_type") != "chat_id" {
				t.Fatalf("receive_id_type = %q", r.URL.Query().Get("receive_id_type"))
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode message body: %v", err)
			}
			seen = append(seen, body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_real_789"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	client := NewMediaClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	fileKey, err := client.UploadFile(context.Background(), []byte("%PDF"), "report.pdf")
	if err != nil {
		t.Fatalf("UploadFile() error = %v", err)
	}
	if fileKey != "file_real_456" {
		t.Fatalf("file key = %q", fileKey)
	}
	if fileType != "pdf" || fileName != "report.pdf" || filePayload != "%PDF" {
		t.Fatalf("file_type=%q file_name=%q payload=%q", fileType, fileName, filePayload)
	}

	imageMessageID, err := client.SendImageMessage(context.Background(), "oc_chat_1", "img_real_123")
	if err != nil {
		t.Fatalf("SendImageMessage() error = %v", err)
	}
	fileMessageID, err := client.SendFileMessage(context.Background(), "oc_chat_1", "file_real_456")
	if err != nil {
		t.Fatalf("SendFileMessage() error = %v", err)
	}
	if imageMessageID != "om_real_789" || fileMessageID != "om_real_789" {
		t.Fatalf("message ids image=%q file=%q", imageMessageID, fileMessageID)
	}
	if len(seen) != 2 {
		t.Fatalf("message bodies = %+v", seen)
	}
	assertMessageContent(t, seen[0], "image", "image_key", "img_real_123")
	assertMessageContent(t, seen[1], "file", "file_key", "file_real_456")
}

func TestOutboundMediaDispatcherUploadsRealLocalImageOncePerChat(t *testing.T) {
	tmp := t.TempDir()
	imagePath := filepath.Join(tmp, "plot.png")
	if err := os.WriteFile(imagePath, []byte("PNGDATA"), 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}
	watcher := NewImageBlockWatcher()
	uploads := watcher.Feed("![plot](" + imagePath + ")")
	if len(uploads) != 1 {
		t.Fatalf("watcher uploads = %+v", uploads)
	}

	var imageUploads int
	var imageMessages int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/im/v1/images":
			imageUploads++
			requireMultipartFilePayload(t, r, "image", "PNGDATA")
			_, _ = w.Write([]byte(`{"code":0,"data":{"image_key":"img_dedup"}}`))
		case "/open-apis/im/v1/messages":
			imageMessages++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			assertMessageContent(t, body, "image", "image_key", "img_dedup")
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_image"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	media := NewMediaClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	dispatcher := NewOutboundMediaDispatcher(media)
	if err := dispatcher.DispatchImage(context.Background(), "oc_chat_1", uploads[0]); err != nil {
		t.Fatalf("DispatchImage() error = %v", err)
	}
	if err := dispatcher.DispatchImage(context.Background(), "oc_chat_1", uploads[0]); err != nil {
		t.Fatalf("second DispatchImage() error = %v", err)
	}
	if imageUploads != 1 || imageMessages != 1 {
		t.Fatalf("imageUploads=%d imageMessages=%d", imageUploads, imageMessages)
	}
}

func TestOutboundMediaDispatcherUploadsBase64FileAndRejectsRemoteFileURL(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("CSV,DATA"))
	watcher := NewFileBlockWatcher()
	uploads := watcher.Feed("[report.csv](data:text/csv;base64," + data + ")")
	if len(uploads) != 1 {
		t.Fatalf("watcher uploads = %+v", uploads)
	}
	remote := PendingUpload{
		ID:   "remote-file",
		Kind: PendingUploadFile,
		Source: UploadSource{
			Kind: UploadSourceURL,
			URL:  "https://example.com/report.csv",
		},
		Alt: "report.csv",
	}

	var filePayload string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/im/v1/files":
			filePayload = requireMultipartFilePayload(t, r, "file", "CSV,DATA")
			_, _ = w.Write([]byte(`{"code":0,"data":{"file_key":"file_csv"}}`))
		case "/open-apis/im/v1/messages":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			assertMessageContent(t, body, "file", "file_key", "file_csv")
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_file"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	media := NewMediaClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	dispatcher := NewOutboundMediaDispatcher(media)
	if err := dispatcher.DispatchFile(context.Background(), "oc_chat_1", uploads[0]); err != nil {
		t.Fatalf("DispatchFile() error = %v", err)
	}
	if filePayload != "CSV,DATA" {
		t.Fatalf("file payload = %q", filePayload)
	}
	if err := dispatcher.DispatchFile(context.Background(), "oc_chat_1", remote); err == nil || !strings.Contains(err.Error(), "remote file URL") {
		t.Fatalf("remote file URL error = %v", err)
	}
}

func TestOutboundMediaDispatcherFetchesRemoteImageWithRealHTTP(t *testing.T) {
	imageSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("REMOTEPNG"))
	}))
	defer imageSource.Close()

	var uploadedPayload string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/im/v1/images":
			uploadedPayload = requireMultipartFilePayload(t, r, "image", "REMOTEPNG")
			_, _ = w.Write([]byte(`{"code":0,"data":{"image_key":"img_remote"}}`))
		case "/open-apis/im/v1/messages":
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_remote"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	media := NewMediaClient(api.Client(), api.URL, func(context.Context) (string, error) {
		return "tenant-token", nil
	})
	dispatcher := NewOutboundMediaDispatcher(media)
	dispatcher.AllowPrivateImageURLs = true
	pending := PendingUpload{
		ID:   "remote-image",
		Kind: PendingUploadImage,
		Source: UploadSource{
			Kind: UploadSourceURL,
			URL:  imageSource.URL + "/plot.png",
		},
	}
	if err := dispatcher.DispatchImage(context.Background(), "oc_chat_1", pending); err != nil {
		t.Fatalf("DispatchImage() error = %v", err)
	}
	if uploadedPayload != "REMOTEPNG" {
		t.Fatalf("uploaded payload = %q", uploadedPayload)
	}
}

func assertMessageContent(t *testing.T, body map[string]any, msgType string, contentKey string, contentValue string) {
	t.Helper()
	if body["receive_id"] != "oc_chat_1" || body["msg_type"] != msgType {
		t.Fatalf("message body = %+v", body)
	}
	var content map[string]string
	if err := json.Unmarshal([]byte(body["content"].(string)), &content); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if content[contentKey] != contentValue {
		t.Fatalf("content = %+v", content)
	}
}

func requireMultipartFilePayload(t *testing.T, r *http.Request, field string, expected string) string {
	t.Helper()
	reader, err := r.MultipartReader()
	if err != nil {
		t.Fatalf("multipart reader: %v", err)
	}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next multipart part: %v", err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		if part.FormName() == field {
			if string(data) != expected {
				t.Fatalf("%s payload = %q", field, string(data))
			}
			return string(data)
		}
	}
	t.Fatalf("multipart field %q not found", field)
	return ""
}
