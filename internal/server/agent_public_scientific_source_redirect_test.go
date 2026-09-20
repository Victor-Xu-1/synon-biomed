package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/securefetch"
	"synon-go/internal/tools/webfetch"
)

func TestAgentPublicScientificCanonicalRedirectHandoff(t *testing.T) {
	for _, externalized := range []bool{false, true} {
		name := "inline"
		if externalized {
			name = "externalized"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newAgentSaveArtifactsFixture(t)
			const canonical = "https://archive.example.org/releases/data.tar"
			const signed = "https://assets.example.org/blob/opaque?signature=old"
			result := scientificRedirectReceipt(canonical, signed)
			raw, err := json.Marshal(map[string]any{"ok": true, "result": result})
			if err != nil {
				t.Fatal(err)
			}
			if externalized {
				record, err := fixture.store.WriteRunnerLargeToolResult(context.Background(), workspace.WriteRunnerLargeToolResultInput{
					ArtifactID: "large-tool-result-0123456789abcdef0123456789abcdef", ProjectID: fixture.stream.ProjectID,
					RootFrameID: fixture.stream.RootFrameID, FrameID: fixture.stream.FrameID,
					StreamUID: fixture.stream.UID, OwnerUserID: fixture.stream.OwnerID,
					RunnerID: fixture.claim.RunnerID, ClaimToken: fixture.claim.ClaimToken,
					Attempt: fixture.claim.Attempt, SourceEventID: 1,
					ToolName: "web_fetch", ToolCallID: "redirect-source", Content: raw,
				})
				if err != nil {
					t.Fatal(err)
				}
				raw, err = json.Marshal(map[string]any{
					"artifact_id": record.ArtifactID, "version_id": record.VersionID,
					"sha256": record.ContentSHA256, "size_bytes": record.SizeBytes,
					"content_type": record.ContentType, "outcome": "partial",
					"content_url": "/api/artifacts/" + record.ArtifactID + "/versions/" + record.VersionID,
					"preview":     "bounded archive prefix", "truncated": true,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			appendAgentPublicScientificSourceCheckpoint(t, fixture, "redirect-source", canonical, false, func(payload map[string]any) {
				payload["toolName"] = "web_fetch"
				sourceInput, _ := json.Marshal(map[string]any{"url": canonical})
				payload["toolInput"] = json.RawMessage(sourceInput)
				payload["toolResult"] = json.RawMessage(raw)
			})
			candidates, err := fixture.server.agentPublicScientificDownloadCandidates(context.Background(), fixture.stream.UID, fixture.stream.OwnerID, 4)
			if err != nil || len(candidates) != 1 || candidates[0].URL != canonical {
				t.Errorf("canonical recovery candidates=%#v err=%v", candidates, err)
			}
			fetcher := &agentPublicScientificFileFetcher{fetch: func(_ context.Context, target string, policy securefetch.Policy) (*securefetch.Response, error) {
				if target != canonical || !sameStringSet(policy.AllowedHosts, []string{"archive.example.org", "assets.example.org"}) {
					t.Errorf("handoff target=%q hosts=%v", target, policy.AllowedHosts)
				}
				// A fresh redirect may rotate the signed URL without changing the
				// attested destination host. The canonical request remains stable.
				final, _ := url.Parse("https://assets.example.org/blob/new?signature=renewed")
				return &securefetch.Response{Body: io.NopCloser(strings.NewReader("tar payload")), FinalURL: final,
					StatusCode: http.StatusOK, ContentType: "application/x-tar", ContentLength: 11}, nil
			}}
			fixture.server.publicScientificFiles = fetcher
			fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
			input := map[string]any{"url": canonical, "human_description": "Downloading the complete archive"}
			gateway := serverAgentRuntimeToolGateway{server: fixture.server,
				taskRun: &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream}}}
			if admission := gateway.agentRuntimePublicScientificSourcePreflight("download_public_scientific_file", input); admission != nil {
				t.Fatalf("canonical handoff rejected before execution: %#v", admission)
			}
			ctx := agentPublicScientificToolContext(t, fixture, "redirect-download", input)
			download, err := fixture.server.executeAgentPublicScientificFileDownload(ctx, fixture.identity, "redirect-download", input)
			if err != nil || len(fetcher.callSnapshot()) != 1 {
				t.Fatalf("download=%#v err=%v calls=%v", download, err, fetcher.callSnapshot())
			}
		})
	}
}

func TestAgentPublicScientificRedirectAuthorityRejectsUnattestedChains(t *testing.T) {
	const canonical = "https://archive.example.org/releases/data.tar"
	const final = "https://assets.example.org/blob/opaque?signature=old"
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing hops", func(v map[string]any) { delete(v, "redirects") }},
		{"wrong origin", func(v map[string]any) { v["requestedUrl"] = "https://other.example.org/data.tar" }},
		{"wrong final", func(v map[string]any) { v["url"] = "https://other.example.org/blob" }},
		{"not a redirect", func(v map[string]any) { v["redirects"].([]any)[0].(map[string]any)["statusCode"] = 200 }},
		{"failed response", func(v map[string]any) { v["statusCode"] = 403 }},
		{"failed envelope", func(v map[string]any) { v["ok"] = false }},
		{"failed status", func(v map[string]any) { v["status"] = "failed" }},
		{"failure payload", func(v map[string]any) { v["failure"] = map[string]any{"code": "transport_failed"} }},
		{"unavailable", func(v map[string]any) { v["sourceUnavailable"] = true }},
		{"private destination", func(v map[string]any) {
			v["url"] = "https://127.0.0.1/private"
			v["redirects"].([]any)[0].(map[string]any)["to"] = v["url"]
		}},
		{"insecure destination", func(v map[string]any) {
			v["url"] = "http://assets.example.org/blob"
			v["redirects"].([]any)[0].(map[string]any)["to"] = v["url"]
		}},
		{"discontinuous chain", func(v map[string]any) {
			v["redirects"] = append(v["redirects"].([]any), map[string]any{"from": canonical, "to": final, "statusCode": 302})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := scientificRedirectReceipt(canonical, final)
			test.mutate(result)
			if agentPublicScientificResultAuthorizesDownload(result, canonical) {
				t.Fatal("unattested chain authorized its claimed request URL")
			}
		})
	}
	result := scientificRedirectReceipt(canonical, final)
	result["body"] = `{"url":"https://unrelated.example.org/extra.tar"}`
	if agentPublicScientificResultAuthorizesDownload(result, "https://unrelated.example.org/extra.tar") {
		t.Fatal("partial response body expanded source authority")
	}
}

func scientificRedirectReceipt(canonical, final string) map[string]any {
	return map[string]any{
		"requestedUrl": canonical, "url": final, "statusCode": 200,
		"contentType": "application/x-tar", "partial": true, "truncated": true,
		"binary": true, "bytesRead": 512, "recovery": "use_dedicated_download_or_fulltext_tool",
		"redirects": []any{map[string]any{"from": canonical, "to": final, "statusCode": 302}},
	}
}

func TestAgentPublicScientificRedirectBindingRequiresExecutedSourceInput(t *testing.T) {
	const canonical = "https://archive.example.org/releases/data.tar"
	const final = "https://assets.example.org/blob?signature=old"
	for _, toolName := range []string{"web_fetch", "mcp__omics-archives__geo_get_series"} {
		t.Run(toolName, func(t *testing.T) {
			fixture := newAgentSaveArtifactsFixture(t)
			appendAgentPublicScientificSourceCheckpoint(t, fixture, "unattested-source", canonical, false, func(payload map[string]any) {
				payload["toolName"] = toolName
				// This input does not attest that WebFetch requested canonical.
				input := json.RawMessage(`{"url":"https://unrelated.example.org/page"}`)
				raw, _ := json.Marshal(scientificRedirectReceipt(canonical, final))
				payload["toolInput"], payload["toolResult"] = input, json.RawMessage(raw)
				payload["requestSha256"], payload["resultSha256"] = kernelMCPEvidenceSHA256(input), kernelMCPEvidenceSHA256(raw)
			})
			request, err := parseAgentPublicScientificFileRequest(map[string]any{"url": canonical, "human_description": "Checking source"})
			if err != nil {
				t.Fatal(err)
			}
			binding, err := fixture.server.validateAgentPublicScientificSourceURL(context.Background(), fixture.stream.UID, fixture.stream.OwnerID, request)
			if err != nil || len(binding.RedirectHosts) != 0 {
				t.Fatalf("unattested transport widened redirect hosts: %#v err=%v", binding, err)
			}
		})
	}
}

func TestAgentPublicScientificExternalizedPartialSourceIntegrity(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	raw, _ := json.Marshal(scientificRedirectReceipt("https://archive.example.org/data.tar", "https://assets.example.org/blob?token=old"))
	record, err := fixture.store.WriteRunnerLargeToolResult(context.Background(), workspace.WriteRunnerLargeToolResultInput{
		ArtifactID: "large-tool-result-abcdef0123456789abcdef0123456789", ProjectID: fixture.stream.ProjectID,
		RootFrameID: fixture.stream.RootFrameID, FrameID: fixture.stream.FrameID,
		StreamUID: fixture.stream.UID, OwnerUserID: fixture.stream.OwnerID,
		RunnerID: fixture.claim.RunnerID, ClaimToken: fixture.claim.ClaimToken, Attempt: fixture.claim.Attempt,
		SourceEventID: 1, ToolName: "web_fetch", ToolCallID: "partial-source", Content: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"sha256", "outcome", "owner"} {
		t.Run(field, func(t *testing.T) {
			descriptor := map[string]any{
				"artifact_id": record.ArtifactID, "version_id": record.VersionID,
				"sha256": record.ContentSHA256, "size_bytes": record.SizeBytes,
				"content_type": record.ContentType, "outcome": "partial",
				"content_url": "/api/artifacts/" + record.ArtifactID + "/versions/" + record.VersionID,
				"preview":     "partial archive", "truncated": true,
			}
			owner := fixture.stream.OwnerID
			switch field {
			case "sha256":
				descriptor[field] = strings.Repeat("0", 64)
			case "outcome":
				descriptor[field] = "failed"
			case "owner":
				owner = "different-owner"
			}
			descriptorRaw, _ := json.Marshal(descriptor)
			if _, err := fixture.server.agentPublicScientificSourceResult(context.Background(), owner, descriptorRaw); err == nil {
				t.Fatalf("partial source accepted invalid %s", field)
			}
		})
	}
}

func TestAgentPublicScientificCanonicalRedirectHandoffRealTLS(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const canonical = "https://archive.example.org/releases/data.tar"
	content := strings.Repeat("archive-content", 512)
	var redirects atomic.Int32
	var resumed atomic.Bool
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "archive.example.org":
			generation := redirects.Add(1)
			http.Redirect(w, r, "https://assets.example.org/object?signature="+strconv.Itoa(int(generation)), http.StatusFound)
		case "assets.example.org":
			w.Header().Set("Content-Type", "application/x-tar")
			w.Header().Set("ETag", `"immutable-archive"`)
			w.Header().Set("Content-Length", strconv.Itoa(len(content)))
			if r.URL.Query().Get("signature") == "2" {
				// The first dedicated transfer closes early. The existing stage
				// must keep these bytes and renew the redirect before resuming.
				_, _ = io.WriteString(w, content[:1024])
				return
			}
			if r.URL.Query().Get("signature") == "3" {
				if r.Header.Get("Range") != "bytes=1024-" || r.Header.Get("If-Range") != `"immutable-archive"` {
					t.Errorf("resume headers=%v", r.Header)
				}
				resumed.Store(true)
				w.Header().Set("Content-Length", strconv.Itoa(len(content)-1024))
				w.Header().Set("Content-Range", "bytes 1024-"+strconv.Itoa(len(content)-1)+"/"+strconv.Itoa(len(content)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = io.WriteString(w, content[1024:])
				return
			}
			_, _ = io.WriteString(w, content)
		default:
			t.Errorf("unattested destination %q", r.Host)
			http.Error(w, "unexpected host", http.StatusBadRequest)
		}
	}))
	t.Cleanup(upstream.Close)
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true // Test-only virtual hosts served by one local TLS fixture.
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	receipt, err := webfetch.FetchWithOptions(context.Background(), canonical, 512, webfetch.Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) {
			clone := *client
			return &clone, nil
		},
	})
	if err != nil || !receipt.Partial || len(receipt.Redirects) != 1 {
		t.Fatalf("real source receipt=%#v err=%v", receipt, err)
	}
	raw, _ := json.Marshal(map[string]any{"ok": true, "result": receipt})
	appendAgentPublicScientificSourceCheckpoint(t, fixture, "tls-source", canonical, false, func(payload map[string]any) {
		payload["toolName"] = "web_fetch"
		input, _ := json.Marshal(map[string]any{"url": canonical, "limit": 512})
		payload["toolInput"], payload["toolResult"] = json.RawMessage(input), json.RawMessage(raw)
	})
	fixture.server.publicScientificFiles = securefetch.New(securefetch.Options{
		HTTPClient: client, TestOnlyAllowCustomTransport: true, Resolver: scientificRedirectTestResolver{},
	})
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	input := map[string]any{"url": canonical, "human_description": "Downloading the complete archive"}
	ctx := agentPublicScientificToolContext(t, fixture, "tls-download", input)
	result, err := fixture.server.executeAgentPublicScientificFileDownload(ctx, fixture.identity, "tls-download", input)
	if err != nil || result["ok"] != true {
		t.Fatalf("real download=%#v err=%v", result, err)
	}
	stored, err := os.ReadFile(filepath.Join(fixture.projectPath, "data.tar"))
	if err != nil || string(stored) != content || redirects.Load() != 3 || !resumed.Load() {
		t.Fatalf("stored_bytes=%d err=%v canonical_requests=%d resumed=%t", len(stored), err, redirects.Load(), resumed.Load())
	}
}

type scientificRedirectTestResolver struct{}

func (scientificRedirectTestResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}
