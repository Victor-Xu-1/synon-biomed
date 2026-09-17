package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	compute "synon-go/internal/compute"
	workspace "synon-go/internal/persistence/workspace"
)

func TestComputeRemoteFilesRealSSHAndSFTPLifecycle(t *testing.T) {
	remoteRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(remoteRoot, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	remoteFile := filepath.Join(remoteRoot, "report.txt")
	if err := os.WriteFile(remoteFile, []byte("remote-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	largeFile := filepath.Join(remoteRoot, "large.bin")
	file, err := os.OpenFile(largeFile, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(compute.RemoteImportMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	dialer, _, closeServer := startLoopbackSFTPServer(t)
	defer closeServer()

	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.UpsertSSHProvider(workspace.ComputeProviderInput{
		Name: "ssh:loopback", UserID: "owner-a", Family: "ssh", ScratchRoot: remoteRoot,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetComputeProviderScratchRoot("ssh:loopback", "owner-a", &remoteRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "remote-project", UserID: "owner-a", Name: "Remote import"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "remote-root", ProjectID: "remote-project", AgentName: "OPERON", Status: "processing", ConversationType: "chat",
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store, FileRoot: t.TempDir(), ComputeRemoteDialer: dialer}).Handler()

	listResponse := remoteRequest(t, app, http.MethodGet,
		"/api/compute/providers/ssh:loopback/files?path="+urlQueryEscape(remoteRoot), nil, "owner-a")
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	var listing struct {
		Entries      []compute.RemoteEntry `json:"entries"`
		Roots        map[string]any        `json:"roots"`
		ResolvedPath string                `json:"resolvedPath"`
	}
	decodeResponseJSON(t, listResponse, &listing)
	if listing.ResolvedPath != remoteRoot || len(listing.Entries) != 3 ||
		!listing.Entries[0].IsDirectory || listing.Entries[1].Name != "large.bin" ||
		listing.Roots["scratch"] != remoteRoot {
		t.Fatalf("listing=%#v", listing)
	}

	downloadResponse := remoteRequest(t, app, http.MethodGet,
		"/api/compute/providers/ssh:loopback/download?path="+urlQueryEscape(remoteFile), nil, "owner-a")
	downloadResponse.Result().Body.Close()
	rangeRequest := httptest.NewRequest(http.MethodGet,
		"/api/compute/providers/ssh:loopback/download?path="+urlQueryEscape(remoteFile), nil)
	rangeRequest.Header.Set("X-Synon-User-Id", "owner-a")
	rangeRequest.Header.Set("Range", "bytes=7-13")
	rangeResponse := httptest.NewRecorder()
	app.ServeHTTP(rangeResponse, rangeRequest)
	if rangeResponse.Code != http.StatusPartialContent || rangeResponse.Body.String() != "fixture" ||
		!strings.Contains(rangeResponse.Header().Get("Content-Disposition"), "report.txt") {
		t.Fatalf("range status=%d headers=%v body=%q", rangeResponse.Code, rangeResponse.Header(), rangeResponse.Body.String())
	}

	importBody := map[string]any{
		"path": remoteFile, "projectId": "remote-project",
	}
	imported := requestComputeWithHeaders(t, app, http.MethodPost, "/api/compute/providers/ssh:loopback/import", importBody, "owner-a", map[string]string{
		"Idempotency-Key": "remote-import-fixture-1",
	})
	if imported.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", imported.Code, imported.Body.String())
	}
	var artifact map[string]any
	decodeResponseJSON(t, imported, &artifact)
	if artifact["filename"] != "report.txt" || artifact["size_bytes"] != float64(len("remote-fixture")) ||
		artifact["root_frame_id"] != "remote-root" || artifact["is_user_upload"] != true ||
		artifact["frame_id"] != nil || artifact["creating_frame_id"] != nil ||
		!filepath.IsAbs(artifact["file_path"].(string)) {
		t.Fatalf("artifact=%#v", artifact)
	}
	lineage, found, err := store.GetCurrentArtifactLineageRecord(artifact["id"].(string), false)
	if err != nil || !found || !lineage.IsUserUpload || lineage.RootFrameID != "remote-root" {
		t.Fatalf("lineage=%#v found=%v err=%v", lineage, found, err)
	}
	outboxCount, err := store.CountOutboxEvents(context.Background())
	if err != nil || outboxCount != 2 {
		t.Fatalf("outbox count=%d err=%v", outboxCount, err)
	}
	replayed := requestComputeWithHeaders(t, app, http.MethodPost, "/api/compute/providers/ssh:loopback/import", importBody, "owner-a", map[string]string{
		"Idempotency-Key": "remote-import-fixture-1",
	})
	if replayed.Code != http.StatusOK {
		t.Fatalf("replayed import status=%d body=%s", replayed.Code, replayed.Body.String())
	}
	var replayedArtifact map[string]any
	decodeResponseJSON(t, replayed, &replayedArtifact)
	if replayedArtifact["id"] != artifact["id"] || replayedArtifact["version_id"] != artifact["version_id"] {
		t.Fatalf("replayed artifact=%#v first=%#v", replayedArtifact, artifact)
	}
	if replayedOutboxCount, err := store.CountOutboxEvents(context.Background()); err != nil || replayedOutboxCount != outboxCount {
		t.Fatalf("replayed outbox count=%d want=%d err=%v", replayedOutboxCount, outboxCount, err)
	}
	if err := os.WriteFile(remoteFile, []byte("changed-remote-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	conflict := requestComputeWithHeaders(t, app, http.MethodPost, "/api/compute/providers/ssh:loopback/import", importBody, "owner-a", map[string]string{
		"Idempotency-Key": "remote-import-fixture-1",
	})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed replay status=%d want=%d body=%s", conflict.Code, http.StatusConflict, conflict.Body.String())
	}

	tooLarge := remoteRequest(t, app, http.MethodPost, "/api/compute/providers/ssh:loopback/import", map[string]any{
		"path": largeFile, "projectId": "remote-project",
	}, "owner-a")
	assertRemoteError(t, tooLarge, http.StatusBadRequest, "too_large")
	relative := remoteRequest(t, app, http.MethodGet,
		"/api/compute/providers/ssh:loopback/files?path=relative", nil, "owner-a")
	assertRemoteError(t, relative, http.StatusBadRequest, "outside_roots")
	missing := remoteRequest(t, app, http.MethodGet,
		"/api/compute/providers/ssh:loopback/files?path="+urlQueryEscape(filepath.Join(remoteRoot, "missing")), nil, "owner-a")
	assertRemoteError(t, missing, http.StatusNotFound, "not_found")
	foreign := remoteRequest(t, app, http.MethodGet,
		"/api/compute/providers/ssh:loopback/files?path="+urlQueryEscape(remoteRoot), nil, "owner-b")
	if foreign.Code != http.StatusBadRequest ||
		!strings.Contains(foreign.Body.String(), "provider 'ssh:loopback' is not an SSH host") {
		t.Fatalf("foreign status=%d body=%s", foreign.Code, foreign.Body.String())
	}
}

func TestOpenSSHRemoteDialerUsesRealSFTPSubsystem(t *testing.T) {
	remoteRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(remoteRoot, "production.txt"), []byte("production-dialer"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, address, closeServer := startLoopbackSFTPServer(t)
	defer closeServer()
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "ssh_config")
	config := "Host production-loopback\n" +
		"  HostName " + host + "\n" +
		"  Port " + port + "\n" +
		"  User fixture\n" +
		"  PreferredAuthentications none\n" +
		"  StrictHostKeyChecking no\n" +
		"  UserKnownHostsFile /dev/null\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	listing, err := compute.ListRemoteDirectory(context.Background(), compute.OpenSSHRemoteDialer{
		Executable: "/usr/bin/ssh", ConfigPath: configPath, Timeout: 5 * time.Second,
	}, "production-loopback", remoteRoot, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Name != "production.txt" {
		t.Fatalf("listing=%#v", listing)
	}
}

func startLoopbackSFTPServer(t *testing.T) (compute.RemoteDialer, string, func()) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	done := make(chan struct{})
	wait.Add(1)
	go func() {
		defer wait.Done()
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			wait.Add(1)
			go func() {
				defer wait.Done()
				serveLoopbackSSHConnection(connection, config)
			}()
		}
	}()
	dialer := &loopbackSFTPDialer{
		address: listener.Addr().String(),
		config: &ssh.ClientConfig{
			User: "fixture", HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second,
		},
	}
	return dialer, listener.Addr().String(), func() {
		close(done)
		_ = listener.Close()
		dialer.closeClients()
		wait.Wait()
	}
}

func serveLoopbackSSHConnection(connection net.Conn, config *ssh.ServerConfig) {
	server, channels, requests, err := ssh.NewServerConn(connection, config)
	if err != nil {
		_ = connection.Close()
		return
	}
	defer server.Close()
	go ssh.DiscardRequests(requests)
	for channelRequest := range channels {
		if channelRequest.ChannelType() != "session" {
			_ = channelRequest.Reject(ssh.UnknownChannelType, "session required")
			continue
		}
		channel, requests, err := channelRequest.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for request := range requests {
				var subsystem struct{ Name string }
				if request.Type != "subsystem" || ssh.Unmarshal(request.Payload, &subsystem) != nil || subsystem.Name != "sftp" {
					_ = request.Reply(false, nil)
					continue
				}
				_ = request.Reply(true, nil)
				server, err := sftp.NewServer(channel)
				if err == nil {
					_ = server.Serve()
					_ = server.Close()
				}
				return
			}
		}()
	}
}

type loopbackSFTPDialer struct {
	address string
	config  *ssh.ClientConfig
	mu      sync.Mutex
	clients map[*loopbackSFTPClient]struct{}
}

func (d *loopbackSFTPDialer) Dial(ctx context.Context, _ string) (compute.RemoteFileClient, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", d.address)
	if err != nil {
		return nil, err
	}
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, d.address, d.config)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	sshClient := ssh.NewClient(clientConnection, channels, requests)
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		return nil, err
	}
	client := &loopbackSFTPClient{Client: sftpClient, sshClient: sshClient, owner: d}
	d.mu.Lock()
	if d.clients == nil {
		d.clients = map[*loopbackSFTPClient]struct{}{}
	}
	d.clients[client] = struct{}{}
	d.mu.Unlock()
	return client, nil
}

func (d *loopbackSFTPDialer) closeClients() {
	d.mu.Lock()
	clients := make([]*loopbackSFTPClient, 0, len(d.clients))
	for client := range d.clients {
		clients = append(clients, client)
	}
	d.mu.Unlock()
	for _, client := range clients {
		_ = client.Close()
	}
}

type loopbackSFTPClient struct {
	*sftp.Client
	sshClient *ssh.Client
	owner     *loopbackSFTPDialer
	once      sync.Once
}

func (c *loopbackSFTPClient) Open(name string) (io.ReadCloser, error) {
	return c.Client.Open(name)
}

func (c *loopbackSFTPClient) Close() error {
	var result error
	c.once.Do(func() {
		result = errors.Join(c.Client.Close(), c.sshClient.Close())
		c.owner.mu.Lock()
		delete(c.owner.clients, c)
		c.owner.mu.Unlock()
	})
	return result
}

func remoteRequest(t *testing.T, handler http.Handler, method, target string, body any, userID string) *httptest.ResponseRecorder {
	t.Helper()
	response := requestCompute(t, handler, method, target, body, userID)
	return response
}

func decodeResponseJSON(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func assertRemoteError(t *testing.T, response *httptest.ResponseRecorder, status int, kind string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	var body map[string]any
	decodeResponseJSON(t, response, &body)
	if body["remoteKind"] != kind || strings.TrimSpace(body["detail"].(string)) == "" {
		t.Fatalf("remote error=%#v", body)
	}
}

func urlQueryEscape(value string) string {
	return url.QueryEscape(value)
}
