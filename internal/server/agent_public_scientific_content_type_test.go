package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"synon-go/internal/tools/securefetch"
)

func TestPublicDownloadResumeMIMEParameters(t *testing.T) {
	for _, item := range []struct {
		before, after string
		valid         bool
	}{
		{"text/html", "text/html; charset=utf-16le", true},
		{"text/html; charset=UTF-8", "text/html; charset=utf-8", true},
		{"text/html; charset=utf-8; profile=source", "text/html; profile=source; charset=UTF-8", true},
		{"text/html; charset=utf-8", "text/html; charset=utf-16le", false},
		{"text/html; charset=utf-8", "text/html", false},
		{"text/html; profile=source", "text/html; profile=other", false},
		{"text/html", "application/xhtml+xml", false},
		{"text/html; charset=\"unfinished", "text/html", false},
	} {
		if got := agentPublicScientificResumeContentTypeMatches(item.before, item.after); got != item.valid {
			t.Fatalf("%q -> %q matched=%t", item.before, item.after, got)
		}
	}
}

func TestPublicHTMLTransferCharsetResumePreservesOriginalBytes(t *testing.T) {
	for _, mode := range []string{"same", "legacy", "changed", "missing", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newAgentSaveArtifactsFixture(t)
			const source = "https://download.example.org/page.html"
			const body = "<html><body>original source λ</body></html>"
			const retained = 8
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != "bytes=8-" || r.Header.Get("If-Range") != `"source-v1"` {
					t.Errorf("resume binding lost: %v", r.Header)
				}
				media := "text/html; charset=UTF-8"
				if mode == "changed" || mode == "replacement" {
					media = "text/html; charset=windows-1252"
				}
				if mode == "missing" {
					media = "text/html"
				}
				w.Header().Set("Content-Type", media)
				w.Header().Set("ETag", `"source-v1"`)
				if mode == "replacement" {
					w.Header().Set("ETag", `"source-v2"`)
					_, _ = io.WriteString(w, body)
					return
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", retained, len(body)-1, len(body)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = io.WriteString(w, body[retained:])
			}))
			defer upstream.Close()
			transport := upstream.Client().Transport.(*http.Transport).Clone()
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
			transport.TLSClientConfig.InsecureSkipVerify = true // Public virtual host, local TLS test server only.
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			}
			defer transport.CloseIdleConnections()
			fixture.server.publicScientificFiles = securefetch.New(securefetch.Options{HTTPClient: &http.Client{Transport: transport}, TestOnlyAllowCustomTransport: true, Resolver: scientificRedirectTestResolver{}})
			request, err := parseAgentPublicScientificFileRequest(map[string]any{"url": source, "human_description": "Reading source"})
			if err != nil {
				t.Fatal(err)
			}
			key, err := agentPublicScientificDownloadStageKey(fixture.projectPath, request)
			if err != nil {
				t.Fatal(err)
			}
			stage, err := fixture.server.openAgentPublicScientificDownloadStage(key, request)
			if err != nil {
				t.Fatal(err)
			}
			defer stage.file.Close()
			if _, err := io.WriteString(stage.file, body[:retained]); err != nil {
				t.Fatal(err)
			}
			stage.state.Validator, stage.state.ExpectedTotal, stage.state.ContentType = `"source-v1"`, int64(len(body)), "text/html; charset=utf-8"
			if mode == "legacy" {
				stage.state.ContentType = "text/html"
			}
			if err := stage.persist(); err != nil {
				t.Fatal(err)
			}
			offset := int64(retained)
			complete, err := fixture.server.transferAgentPublicScientificAttempt(context.Background(), fixture.projectPath, request, stage, &offset)
			stored, readErr := os.ReadFile(stage.payload)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "changed" || mode == "missing" {
				if !errors.Is(err, errAgentPublicScientificFileResume) || complete || string(stored) != body[:retained] {
					t.Fatalf("inconsistent resume appended bytes: complete=%t error=%v bytes=%q", complete, err, stored)
				}
				return
			}
			if err != nil || !complete || string(stored) != body || !strings.Contains(strings.ToLower(stage.state.ContentType), "charset=") {
				t.Fatalf("valid transfer failed: complete=%t error=%v content_type=%q bytes=%q", complete, err, stage.state.ContentType, stored)
			}
		})
	}
}
