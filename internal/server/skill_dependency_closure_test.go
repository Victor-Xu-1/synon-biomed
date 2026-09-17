package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	secretstore "synon-go/internal/persistence/secrets"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/skills"
	"synon-go/internal/tools/articlefulltext"
	toolregistry "synon-go/internal/tools/registry"
)

func TestV11DependencyClosureUsesRealGatewayAndConfiguredHostModel(t *testing.T) {
	articleAPI := newPinnedArticleFixture(t)
	defer articleAPI.server.Close()
	searchAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<a class="result__a" href="https://example.org/evidence">Biomedical evidence study</a>`))
	}))
	defer searchAPI.Close()
	t.Setenv("SYNON_WEBSEARCH_ENDPOINT", searchAPI.URL+"/search-web?q={query}")

	modelRequests := atomic.Int64{}
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelRequests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("model request = %s %s", r.Method, r.URL.Path)
			http.Error(w, "bad route", http.StatusBadRequest)
			return
		}
		if authorization := r.Header.Get("Authorization"); authorization != "Bearer closure-secret" {
			t.Errorf("Authorization = %q", authorization)
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode model request: %v", err)
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if request.Model != "closure-model" || len(request.Messages) == 0 {
			t.Errorf("model request = %#v", request)
			http.Error(w, "bad model", http.StatusBadRequest)
			return
		}
		requestJSON, _ := json.Marshal(request.Messages)
		if !strings.Contains(string(requestJSON), "extract structured evidence from article") {
			t.Errorf("messages = %s", requestJSON)
			http.Error(w, "missing prompt", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "closure-model-request")
		_, _ = w.Write([]byte(`{"id":"closure-model-request","choices":[{"message":{"role":"assistant","content":"{\"claims\":[{\"claim\":\"verified effect\",\"grade\":\"A\"}]}"}}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	workspaceStore, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	catalog := skills.Load([]string{filepath.Join("..", "..", "skills", "synonbiomed")})
	if errors := catalog.LoadErrors(); len(errors) != 0 {
		t.Fatalf("skill load errors = %#v", errors)
	}
	reg := toolregistry.DefaultWithArticleFulltextClient(articlefulltext.NewClient(articleAPI.options))
	srv := New(Options{Tools: reg, FileRoot: root, SkillCatalog: catalog, Workspace: workspaceStore})

	unconfigured := srv.v11SkillDependencyClosure()
	unconfiguredDeep := unconfigured.Skill("deep-literature-investigation")
	if unconfiguredDeep.Dependency("host.llm").Name != "" {
		t.Fatalf("deep literature still declares the retired competing host.llm route: %#v", unconfiguredDeep)
	}

	if _, err := srv.workspaceStore.CreateProject(workspace.CreateProjectInput{ID: "closure-project", UserID: "closure-user", Name: "Closure project", Path: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.settingsStore.Set("model.activeProviderId", "closure-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.secretStore.Create(secretstore.Secret{ID: "closure-key", UserID: "closure-user", Provider: "openai", Value: "closure-secret"}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := srv.workspaceStore.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "closure-provider", UserID: "closure-user", Name: "Closure provider", Type: "openai",
		BaseURL: modelAPI.URL + "/v1", Model: "closure-model", SecretRef: "secret://closure-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	session := sessionstore.Session{
		ID: "closure-session", Title: "Closure", WorkDir: root, CreatedAt: now, UpdatedAt: now,
		LastUserMessageAt: now, MessageCount: 1, LastRole: "user",
		Project: &sessionstore.Project{ID: "closure-project", Name: "Closure project", Path: root, BoundAt: now},
	}
	if err := srv.sessionStore.Save(session); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.eventJournal.Append(session.ID, eventjournal.Message{"type": "message", "role": "user", "text": "extract structured evidence from article"}, eventjournal.Metadata{ClientMessageID: "closure-user-message"}); err != nil {
		t.Fatal(err)
	}

	configured := srv.v11SkillDependencyClosure(session.ID)
	// The full catalog is not closed in this environment because v1.1 skills
	// legitimately require external runtimes (python packages, HPC commands)
	// and task-scoped dynamic tools that are only evidence-level here. Deep
	// literature must not regain a second model-extraction route merely because
	// a provider is configured.
	deepLiterature := configured.Skill("deep-literature-investigation")
	if deepLiterature.Dependency("host.llm").Name != "" || deepLiterature.Dependency("web_search").Name == "" || deepLiterature.Dependency("fetch_article_fulltext").Name == "" {
		t.Fatalf("configured deep literature closure = %#v", deepLiterature)
	}

	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	for _, test := range []struct {
		name  string
		input map[string]any
		want  string
	}{
		{name: "ask_user", input: map[string]any{"questions": []any{map[string]any{
			"question": "Continue?", "header": "Closure", "options": []any{
				askUserDecisionOption(
					"Yes", "Continue the verified closure test.", "Completes the current check.", "Uses the current closure route.",
					"Ready from the configured dependency closure.", []any{"source:closure-config"}, "No additional resources.",
					"The closure test continues.", "Recommended because the current route is configured.", true,
				),
				askUserDecisionOption(
					"No", "Stop the closure test.", "Avoids further execution.", "Leaves the check incomplete.",
					"Ready immediately.", []any{"source:closure-config"}, "No additional resources.",
					"The closure test remains stopped.", "Choose when the configured route should not continue.", false,
				),
			}, "multiSelect": false,
		}}}, want: "Continue?"},
		{name: "web_search", input: map[string]any{"query": "biomedical evidence"}, want: "example.org/evidence"},
		{name: "fetch_article_fulltext", input: map[string]any{"pmcid": "PMC900"}, want: "gateway full text"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := executeGatewayTool(t, httpServer.URL, test.name, test.input)
			if !strings.Contains(body, test.want) {
				t.Fatalf("gateway response = %s, want %q", body, test.want)
			}
		})
	}

	cycle, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{SessionID: session.ID, RunnerID: "closure-runner", Endpoint: "http://127.0.0.1:1/v1/chat/completions",
		APIKey: "must-not-be-used", Model: "must-not-be-used", LeaseTTL: time.Minute, ReplayLimit: 20,
		OutputLimitBytes: 64 * 1024, RequestTimeout: time.Minute, MaxAttempts: 1, MaxToolRounds: 1,
	})
	if err != nil || !cycle.Claimed || cycle.Status != "completed" {
		t.Fatalf("runner cycle=%#v error=%v", cycle, err)
	}
	if modelRequests.Load() != 1 {
		t.Fatalf("model requests = %d", modelRequests.Load())
	}
	entries, err := srv.eventJournal.ReadAfter(session.ID, 0, 30)
	if err != nil {
		t.Fatal(err)
	}
	if !hasJournalEntry(providerAuthorityEntriesToAny(entries), "message", "", `"grade":"A"`) {
		t.Fatalf("structured extraction was not persisted: %#v", entries)
	}
	audits, err := srv.runtimeStore.List(sessionRunnerModelAuditRuntimeNamespace)
	if err != nil {
		t.Fatal(err)
	}
	audit := findSessionRunnerModelAuditValue(audits, session.ID)
	if audit == nil || audit["providerAuthority"] != true || audit["providerId"] != "closure-provider" || audit["requestId"] != "closure-model-request" {
		t.Fatalf("provider authority audit = %#v", audit)
	}
}

func TestV11DependencyRuntimeStatusCollapsesLegacyWebResearchAtBoundary(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	canonical := srv.v11DependencyRuntimeStatus("web_research", toolregistry.DependencyClassTool, "")
	if !canonical.Executable || canonical.Availability != "available" || canonical.Route != "unified-agent-runtime" {
		t.Fatalf("canonical status = %#v", canonical)
	}
	legacy := srv.v11DependencyRuntimeStatus("WebResearch", toolregistry.DependencyClassTool, "")
	if legacy != canonical {
		t.Fatalf("legacy status = %#v", legacy)
	}
}

func TestV11DependencyRuntimeStatusIncludesPatentSearchAndPublicDownload(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	patent := fixture.server.v11DependencyRuntimeStatus("patent_search", toolregistry.DependencyClassTool, "")
	if !patent.Executable || patent.Availability != "available" || patent.Route != "unified-agent-runtime" {
		t.Fatalf("patent_search status = %#v", patent)
	}
	download := fixture.server.v11DependencyRuntimeStatusForSession(
		"download_public_scientific_file", toolregistry.DependencyClassTool, "project-save", "frame-save",
	)
	if download.Executable || download.Availability != "conditional" ||
		download.Route != "unified-agent-runtime" || !download.AuthorityResolved {
		t.Fatalf("public download status = %#v", download)
	}
	networkAccess := fixture.server.v11DependencyRuntimeStatusForSession(
		"request_network_access", toolregistry.DependencyClassTool, "project-save", "frame-save",
	)
	if networkAccess.Executable || networkAccess.Availability != "conditional" ||
		networkAccess.Route != "unified-agent-runtime" || !networkAccess.AuthorityResolved {
		t.Fatalf("network access status = %#v", networkAccess)
	}
}

type articleFixture struct {
	server  *httptest.Server
	options articlefulltext.Options
}

func newPinnedArticleFixture(t *testing.T) articleFixture {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search-web":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<a class="result__a" href="https://example.org/evidence">Evidence</a>`))
		case "/PMC900/fullTextXML":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<article>gateway full text</article>`))
		default:
			http.NotFound(w, r)
		}
	}))
	options, _ := articlefulltextPinnedTestOptions(t, server)
	return articleFixture{server: server, options: options}
}

type serverArticleResolver struct{}

func (serverArticleResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
}

func articlefulltextPinnedTestOptions(t *testing.T, server *httptest.Server) (articlefulltext.Options, string) {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true // Synthetic public IP is mapped to this TLS fixture only.
	baseDial := (&net.Dialer{}).DialContext
	transport.DialContext = func(ctx context.Context, network string, address string) (net.Conn, error) {
		_, requestedPort, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return nil, splitErr
		}
		return baseDial(ctx, network, net.JoinHostPort(parsed.Hostname(), requestedPort))
	}
	return articlefulltext.Options{
		BaseURL: "https://www.ebi.ac.uk:" + port, HTTPClient: &http.Client{Transport: transport},
		Resolver: serverArticleResolver{}, TestOnlyAllowCustomOrigin: true,
	}, port
}

func executeGatewayTool(t *testing.T, baseURL string, name string, input map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	response, err := http.Post(baseURL+"/api/tools/"+name+"/execute", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("execute %s: %v", name, err)
	}
	defer response.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode %s response: %v", name, err)
	}
	raw, _ := json.Marshal(decoded)
	if response.StatusCode != http.StatusOK || decoded["ok"] != true {
		t.Fatalf("execute %s status=%d response=%s", name, response.StatusCode, raw)
	}
	return string(raw)
}
