package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/securefetch"
	"synon-go/internal/tools/webfetch"
)

func TestPublicHTMLFullDownloadPreservesBeyondWebPreviewAndReadsTail(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const source = "https://download.example.org/study.html"
	const rows = 65536
	body := "<!doctype html><html><body>\n" + strings.Repeat("<p>"+strings.Repeat("x", 70)+"</p>\n", rows) + "<p>complete-tail-λ</p>\n</body></html>"
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("ETag", `"complete-source"`)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true // Local TLS fixture uses a public virtual hostname.
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	preview, err := webfetch.FetchWithOptions(context.Background(), source, webfetch.MaxResponseLimit, webfetch.Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) { return client, nil },
	})
	if err != nil || !preview.Truncated || strings.Contains(preview.Body, "complete-tail") {
		t.Fatalf("expected bounded preview: truncated=%t error=%v", preview.Truncated, err)
	}
	sourceCall := appendAgentPublicScientificSourceCheckpoint(t, fixture, "html-preview", source, false, func(payload map[string]any) {
		payload["toolName"] = "web_fetch"
		raw, marshalErr := json.Marshal(preview)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		digest := sha256.Sum256([]byte("web_fetch\x00html-preview"))
		record, err := fixture.store.WriteRunnerLargeToolResult(context.Background(), workspace.WriteRunnerLargeToolResultInput{
			ArtifactID: "large-tool-result-" + hex.EncodeToString(digest[:16]), ProjectID: fixture.stream.ProjectID,
			RootFrameID: fixture.stream.RootFrameID, FrameID: fixture.stream.FrameID, StreamUID: fixture.stream.UID,
			OwnerUserID: fixture.stream.OwnerID, RunnerID: fixture.claim.RunnerID, ClaimToken: fixture.claim.ClaimToken,
			Attempt: fixture.claim.Attempt, SourceEventID: 1, ToolName: "web_fetch", ToolCallID: "html-preview", Content: raw,
		})
		if err != nil {
			t.Fatal(err)
		}
		descriptor, err := buildRunnerLargeToolResultDescriptor(context.Background(), agentruntime.LargeToolResultInput{
			ToolCall: agentruntime.ToolCall{ID: "html-preview", Name: "web_fetch"}, RawJSON: raw,
			Outcome: agentruntime.ToolResultPartial, MaxInlineBytes: runnerLargeToolResultInlineLimitBytes,
		}, record)
		if err != nil {
			t.Fatal(err)
		}
		payload["toolResult"] = descriptor
	})
	fixture.server.publicScientificFiles = securefetch.New(securefetch.Options{
		HTTPClient: client, TestOnlyAllowCustomTransport: true, Resolver: scientificRedirectTestResolver{},
	})
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	input := map[string]any{"url": source, "source_tool_call_id": sourceCall, "human_description": "Reading complete source"}
	ctx := agentPublicScientificToolContext(t, fixture, "html-download", input)
	result, err := fixture.server.executeAgentPublicScientificFileDownload(ctx, fixture.identity, "html-download", input)
	if err != nil || result["ok"] != true {
		t.Fatalf("full HTML download: %v %v", result, err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	digest := sha256.Sum256([]byte(body))
	if len(artifacts) != 1 || artifacts[0]["checksum"] != hex.EncodeToString(digest[:]) {
		t.Fatalf("missing exact-byte receipt: %v", artifacts)
	}
	file, err := os.Open(filepath.Join(fixture.projectPath, "study.html"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil || n != int64(len(body)) || hex.EncodeToString(hash.Sum(nil)) != hex.EncodeToString(digest[:]) {
		t.Fatalf("full body mismatch: bytes=%d error=%v", n, err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	read, err := readAgentWorkspaceFile(ctx, file, "study.html", "text/html", n, map[string]any{"offset": rows + 2, "limit": 2})
	if err != nil || !strings.Contains(stringValue(read.(map[string]any)["content"]), "complete-tail-λ") {
		t.Fatalf("tail not readable: %v %v", read, err)
	}
}

func TestPublicHTMLDownloadPreservesExtensionlessPagesAndRejectsSpoofs(t *testing.T) {
	for _, item := range []struct {
		name, media, body string
		status            int
		want              bool
	}{
		{"html", "text/html", "<!doctype html><html><body>Full source</body></html>", 200, true},
		{"utf8-html", "text/html; charset=utf-8", "<!doctype html><html><body>完整来源 λ</body></html>", 200, true},
		{"utf8-bom", "text/html; charset=utf-8", "\xef\xbb\xbf<html><body>完整来源</body></html>", 200, true},
		{"xhtml", "application/xhtml+xml", `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><body>Full source</body></html>`, 200, true},
		{"utf16-xhtml", "application/xhtml+xml; charset=utf-16le", publicHTMLUTF16LE(`<?xml version="1.0" encoding="UTF-16LE"?><html xmlns="http://www.w3.org/1999/xhtml"><body>完整来源</body></html>`), 200, true},
		{"utf16-html", "text/html; charset=utf-16le", "<\x00h\x00t\x00m\x00l\x00>\x00<\x00b\x00o\x00d\x00y\x00>\x00A\x00<\x00/\x00b\x00o\x00d\x00y\x00>\x00<\x00/\x00h\x00t\x00m\x00l\x00>\x00", 200, true},
		{"forbidden", "text/html", "<html><body>Access denied</body></html>", 403, false},
		{"spoofed-body", "text/html", "%PDF-1.7\nnot html", 200, false},
		{"spoofed-type", "application/pdf", "<html><body>Not PDF</body></html>", 200, false},
		{"unknown-charset", "text/html; charset=not-a-real-encoding", "<html><body>Source</body></html>", 200, false},
		{"spoofed-charset", "text/html; charset=utf-16le", "%PDF-1.7\nnot html", 200, false},
		{"ordinary-xml", "application/xhtml+xml; charset=utf-8", `<?xml version="1.0"?><data>Not XHTML</data>`, 200, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			fixture := newAgentSaveArtifactsFixture(t)
			const source = "https://download.example.org/document?view=full"
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RawQuery != "view=full" {
					t.Error("query identity changed")
				}
				w.Header().Set("Content-Type", item.media)
				w.WriteHeader(item.status)
				_, _ = io.WriteString(w, item.body)
			}))
			defer upstream.Close()
			transport := upstream.Client().Transport.(*http.Transport).Clone()
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
			transport.TLSClientConfig.InsecureSkipVerify = true
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			}
			defer transport.CloseIdleConnections()
			fixture.server.publicScientificFiles = securefetch.New(securefetch.Options{HTTPClient: &http.Client{Transport: transport}, TestOnlyAllowCustomTransport: true, Resolver: scientificRedirectTestResolver{}})
			fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
			sourceID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "page-source", source, false, nil)
			input := map[string]any{"url": source, "filename": "page.html", "source_tool_call_id": sourceID, "human_description": "Reading page"}
			result, err := fixture.server.executeAgentPublicScientificFileDownload(agentPublicScientificToolContext(t, fixture, "page-download", input), fixture.identity, "page-download", input)
			if item.want {
				if err != nil || result["ok"] != true {
					t.Fatalf("extensionless download failed: %v %v", result, err)
				}
				version := stringValue(agentSaveArtifactResults(t, result)[0]["version_id"])
				readable, found, readErr := fixture.server.openAgentWorkspaceReadableVersion(context.Background(), fixture.stream.OwnerID, fixture.stream.ProjectID, version)
				if readErr != nil || !found {
					t.Fatalf("saved source unavailable: %v", readErr)
				}
				bytes, readErr := io.ReadAll(readable.reader)
				_ = readable.reader.Close()
				if readErr != nil || string(bytes) != item.body || readable.contentType != item.media {
					t.Fatalf("original encoding lost: type=%q bytes=%q err=%v", readable.contentType, bytes, readErr)
				}
				if !passiveArtifactHTMLSource(fixture.store, version, "text/html") {
					t.Fatal("download lost passive-source metadata")
				}
			} else {
				if err == nil && result["ok"] == true {
					t.Fatalf("invalid page became success: %v", result)
				}
				if _, statErr := os.Stat(filepath.Join(fixture.projectPath, "page.html")); !os.IsNotExist(statErr) {
					t.Fatalf("invalid page was published: %v", statErr)
				}
			}
		})
	}
}

func publicHTMLUTF16LE(text string) string {
	units := utf16.Encode([]rune(text))
	raw := make([]byte, 2*len(units))
	for index, unit := range units {
		raw[index*2], raw[index*2+1] = byte(unit), byte(unit>>8)
	}
	return string(raw)
}

func TestPublicHTMLMediaAdmissionDoesNotWidenNetworkOrFileAuthority(t *testing.T) {
	for _, url := range []string{"http://example.org/page.html", "https://127.0.0.1/page.html", "https://localhost/page.html", "https://example.org:8080/page.html", "https://user:pass@example.org/page.html"} {
		if _, err := parseAgentPublicScientificFileRequest(map[string]any{"url": url, "filename": "page.html", "human_description": "Reading page"}); err == nil {
			t.Fatalf("unsafe source admitted: %s", url)
		}
	}
	if _, err := parseAgentPublicScientificFileRequest(map[string]any{"url": "https://example.org/data.pdf", "filename": "page.html", "human_description": "Reading page"}); err == nil {
		t.Fatal("file format rename bypassed media authority")
	}
}
