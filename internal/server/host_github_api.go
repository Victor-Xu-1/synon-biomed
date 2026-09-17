package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	secretstore "synon-go/internal/persistence/secrets"
)

const (
	hostGitHubCommandTimeout = 3 * time.Second
	hostGitHubOutputLimit    = 64 << 10
)

var (
	hostGitHubTokenPattern  = regexp.MustCompile(`^(gh[opu]_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]{22,})$`)
	hostGitHubLoginPattern  = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}$`)
	errHostGitHubConfigured = errors.New("a GitHub credential is already configured")
)

type hostGitHubCredential struct {
	Token  string
	Login  string
	Source string
}

func (s *Server) handleHostGitHubCredentialProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	store, ok := s.secretStoreForCompatibilityRequest(w)
	if !ok {
		return
	}
	userID := compatAgentUserID(r)
	configured, err := compatibilityGitHubSecretConfigured(store, userID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if configured {
		writeJSON(w, http.StatusOK, map[string]any{"found": false, "reason": "already_configured"})
		return
	}
	credential, found := probeHostGitHubCredential(r.Context())
	if !found {
		writeJSON(w, http.StatusOK, map[string]any{"found": false})
		return
	}
	var login any
	if credential.Login != "" {
		login = credential.Login
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"found": true, "login": login, "masked_preview": maskHostGitHubToken(credential.Token), "source": credential.Source,
	})
}

func (s *Server) handleHostGitHubCredentialUse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	store, ok := s.secretStoreForCompatibilityRequest(w)
	if !ok {
		return
	}
	userID := compatAgentUserID(r)
	configured, err := compatibilityGitHubSecretConfigured(store, userID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if configured {
		writeV11Detail(w, http.StatusConflict, "A GitHub credential is already configured.")
		return
	}
	credential, found := probeHostGitHubCredential(r.Context())
	if !found {
		writeV11Detail(w, http.StatusNotFound, "No host GitHub credential found.")
		return
	}
	created, err := createHostGitHubSecret(store, userID, credential)
	if errors.Is(err, errHostGitHubConfigured) {
		writeV11Detail(w, http.StatusConflict, "A GitHub credential is already configured.")
		return
	}
	if err != nil {
		writeCompatibilitySecretError(w, err, "")
		return
	}
	writeJSON(w, http.StatusCreated, projectCompatibilitySecret(created))
}

func compatibilityGitHubSecretConfigured(store *secretstore.Store, userID string) (bool, error) {
	items, err := store.ListForUser(userID)
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if item.Provider == "github" {
			return true, nil
		}
	}
	return false, nil
}

func createHostGitHubSecret(
	store *secretstore.Store, userID string, credential hostGitHubCredential,
) (secretstore.Secret, error) {
	name := "GitHub (host)"
	if credential.Login != "" {
		name = "github-" + credential.Login
	}
	descriptionSource := "git credential fill"
	if credential.Source == "gh" {
		descriptionSource = "gh auth token"
	}
	candidate := secretstore.Secret{
		ID:                    uuid.NewString(),
		UserID:                userID,
		Provider:              "github",
		Name:                  name,
		Description:           "Discovered from host `" + descriptionSource + "`",
		DescriptionPresent:    true,
		CredentialType:        "host_discovered",
		CredentialTypePresent: true,
	}
	var login any
	if credential.Login != "" {
		login = credential.Login
	}
	if err := candidate.SetCredentialObject(map[string]any{
		"token": credential.Token, "login": login, "auto_discovered": true, "source": credential.Source,
	}); err != nil {
		return secretstore.Secret{}, err
	}
	return store.CreateWithOptions(candidate, secretstore.CreateOptions{
		PreserveNameWhitespace: true,
		Validate: func(existing []secretstore.Secret, candidate *secretstore.Secret) error {
			for _, item := range existing {
				if item.UserID == userID && item.Provider == "github" {
					return errHostGitHubConfigured
				}
			}
			if compatibilitySecretNameExists(existing, userID, candidate.Name, "") {
				return compatibilitySecretErrorf("Secret '%s' already exists", candidate.Name)
			}
			return nil
		},
	})
}

func probeHostGitHubCredential(ctx context.Context) (hostGitHubCredential, bool) {
	token, ok := runHostGitHubCommand(ctx, "gh", []string{"auth", "token", "--hostname", "github.com"}, "", nil)
	token = strings.TrimSpace(token)
	source := "gh"
	if !ok || !hostGitHubTokenPattern.MatchString(token) {
		output, gitOK := runHostGitHubCommand(
			ctx,
			"git",
			[]string{"-c", "credential.interactive=never", "credential", "fill"},
			"protocol=https\nhost=github.com\n\n",
			map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_ASKPASS": "true", "SSH_ASKPASS": "true"},
		)
		token = parseHostGitHubCredentialFill(output)
		if !gitOK || token == "" {
			return hostGitHubCredential{}, false
		}
		source = "git-credential"
	}
	login, loginOK := runHostGitHubCommand(
		ctx,
		"gh",
		[]string{"api", "user", "-q", ".login"},
		"",
		map[string]string{"GH_TOKEN": token, "GH_HOST": "github.com"},
	)
	login = strings.TrimSpace(login)
	if !loginOK || !hostGitHubLoginPattern.MatchString(login) {
		login = ""
	}
	return hostGitHubCredential{Token: token, Login: login, Source: source}, true
}

func parseHostGitHubCredentialFill(output string) string {
	for _, line := range strings.Split(output, "\n") {
		separator := strings.IndexByte(line, '=')
		if separator <= 0 || line[:separator] != "password" {
			continue
		}
		token := strings.TrimSpace(line[separator+1:])
		if hostGitHubTokenPattern.MatchString(token) {
			return token
		}
		return ""
	}
	return ""
}

func runHostGitHubCommand(
	ctx context.Context, name string, args []string, stdin string, overrides map[string]string,
) (string, bool) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	commandContext, cancel := context.WithTimeout(ctx, hostGitHubCommandTimeout)
	defer cancel()
	command := exec.CommandContext(commandContext, path, args...)
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	if len(overrides) > 0 {
		command.Env = hostGitHubCommandEnvironment(overrides)
	}
	stdout := &cappedCommandBuffer{max: hostGitHubOutputLimit}
	stderr := &cappedCommandBuffer{max: hostGitHubOutputLimit}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return "", false
	}
	return stdout.String(), true
}

func hostGitHubCommandEnvironment(overrides map[string]string) []string {
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if _, replaced := overrides[name]; found && replaced {
			continue
		}
		environment = append(environment, entry)
	}
	for name, value := range overrides {
		environment = append(environment, name+"="+value)
	}
	return environment
}

func maskHostGitHubToken(token string) string {
	if len(token) >= 10 {
		return token[:4] + "\u00b7\u00b7\u00b7\u00b7" + token[len(token)-4:]
	}
	return "****\u00b7\u00b7\u00b7\u00b7"
}

type cappedCommandBuffer struct {
	buffer bytes.Buffer
	max    int
}

func (b *cappedCommandBuffer) Write(payload []byte) (int, error) {
	if b.max <= 0 || b.buffer.Len() >= b.max {
		return len(payload), errors.New("command output limit exceeded")
	}
	remaining := b.max - b.buffer.Len()
	if len(payload) > remaining {
		_, _ = b.buffer.Write(payload[:remaining])
		return len(payload), errors.New("command output limit exceeded")
	}
	return b.buffer.Write(payload)
}

func (b *cappedCommandBuffer) String() string {
	return b.buffer.String()
}
