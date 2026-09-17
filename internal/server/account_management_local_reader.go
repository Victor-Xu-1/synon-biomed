package server

import (
	"context"
	"errors"
	"sort"
	"strings"

	"synon-go/internal/account"
	settingsstore "synon-go/internal/persistence/settings"
	"synon-go/internal/synonlink"
)

var errAccountManagementUserNotFound = errors.New("account management user not found")

type localAccountManagementReader struct {
	accounts       *webAccountStore
	sessions       *webSessionStore
	external       *webExternalAuthRuntime
	localAuth      *synonLinkAuthenticator
	settings       *settingsstore.Store
	allowedDomains []string
	deniedDomains  []string
	mcpConfigured  bool
}

func newLocalAccountManagementReader(state webAuthenticationState, localAuth *synonLinkAuthenticator) account.ManagementReader {
	return newLocalAccountManagementReaderWithConfig(state, localAuth, localAccountManagementConfig{})
}

type localAccountManagementConfig struct {
	settings       *settingsstore.Store
	allowedDomains []string
	deniedDomains  []string
	mcpConfigured  bool
}

func newLocalAccountManagementReaderWithConfig(
	state webAuthenticationState,
	localAuth *synonLinkAuthenticator,
	config localAccountManagementConfig,
) account.ManagementReader {
	return &localAccountManagementReader{
		accounts:       state.webAccounts,
		sessions:       state.webSessions,
		external:       state.webExternalAuth,
		localAuth:      localAuth,
		settings:       config.settings,
		allowedDomains: append([]string(nil), config.allowedDomains...),
		deniedDomains:  append([]string(nil), config.deniedDomains...),
		mcpConfigured:  config.mcpConfigured,
	}
}

func (reader *localAccountManagementReader) Read(
	ctx context.Context,
	request account.ManagementReadRequest,
) (account.ManagementSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return account.ManagementSnapshot{}, err
	}
	user, ok := reader.userByID(request.AccountID)
	if !ok {
		return account.ManagementSnapshot{}, errAccountManagementUserNotFound
	}

	sessions := []account.Session{}
	events := []account.SecurityEvent{}
	if reader.sessions != nil && strings.TrimSpace(request.SessionToken) != "" {
		views, err := reader.sessions.List(user.ID, request.SessionToken)
		if err != nil {
			return account.ManagementSnapshot{}, err
		}
		for _, view := range views {
			sessions = append(sessions, account.Session{
				ID: view.ID, AuthMethod: view.AuthMethod, CreatedAt: view.CreatedAt,
				LastSeenAt: view.LastSeenAt, ExpiresAt: view.ExpiresAt, Remembered: view.Remembered,
				UserAgent: view.UserAgent, NetworkClass: view.NetworkClass, Current: view.Current,
			})
			if view.Current {
				user.Provider = view.AuthMethod
			}
		}
		for _, event := range reader.sessions.Events(user.ID, 20) {
			events = append(events, account.SecurityEvent{
				ID: event.ID, SessionID: event.SessionID, Type: event.Type,
				AuthMethod: event.AuthMethod, CreatedAt: event.CreatedAt, Success: event.Success,
				UserAgent: event.UserAgent, NetworkClass: event.NetworkClass,
			})
		}
	}

	emailVerified := false
	if reader.accounts != nil {
		emailVerified = reader.accounts.EmailVerifiedForID(user.ID)
	}
	runtimePolicy := reader.runtimePolicy()
	return account.ManagementSnapshot{
		SchemaVersion: account.ManagementSchemaVersion,
		ControlPlane: account.ControlPlane{
			Mode: "local-runtime", Managed: false, PolicySource: "local-runtime",
			DataResidency: "local", CentralRevocation: false, SessionProtection: reader.sessions != nil,
			OfflineWorkAllowed:  true,
			HighRiskOnlineCheck: false,
		},
		ControlBoundaries: reader.controlBoundaries(),
		RuntimePolicy:     runtimePolicy,
		SecurityPosture: account.SecurityPosture{
			// The local runtime has session protection, but no account-level MFA,
			// Passkey, or recovery-code authority yet.
			MFAEnabled: false, PasskeyEnabled: false, RecoveryConfigured: false,
		},
		Account: account.Account{
			ID: user.ID, Username: user.Username, DisplayName: accountDisplayName(user),
			Email: user.Email, EmailVerified: emailVerified, Provider: user.Provider, Status: "active",
		},
		LoginMethods: accountManagementLoginMethods(reader.accounts, reader.external, reader.localAuth, user.ID),
		Workspaces: []account.Workspace{{
			ID: "local-runtime", Name: "Synon Biomed local workspace", Kind: "local",
			Role: "owner", Status: "active", Current: true,
		}},
		Entitlements:   []account.Entitlement{},
		Sessions:       sessions,
		SecurityEvents: events,
	}, nil
}

func (reader *localAccountManagementReader) controlBoundaries() []account.ControlBoundary {
	return []account.ControlBoundary{
		{ID: "identity", Status: "active", Source: "local-runtime", Managed: false, UserConfigurable: false},
		{ID: "workspace", Status: "active", Source: "local-runtime", Managed: false, UserConfigurable: false},
		{ID: "runtime", Status: "active", Source: "local-runtime", Managed: false, UserConfigurable: true},
		{ID: "entitlements", Status: "not-configured", Source: "none", Managed: false, UserConfigurable: false},
		{ID: "integrations", Status: "not-configured", Source: "none", Managed: false, UserConfigurable: false},
		{ID: "audit", Status: "active", Source: "local-runtime", Managed: false, UserConfigurable: false},
	}
}

func (reader *localAccountManagementReader) runtimePolicy() account.RuntimePolicy {
	approvalMode := "confirm"
	if reader.settings != nil {
		if setting, ok, err := reader.settings.Get(approvalDefaultsSettingKey); err == nil && ok {
			if defaults, normalizeErr := synonlink.NormalizePolicyDefaults(setting.Value); normalizeErr == nil {
				if normalized := strings.ToLower(strings.TrimSpace(defaults.Mode)); normalized != "" {
					approvalMode = normalized
				}
			}
		}
	}
	networkMode := "default"
	if len(reader.allowedDomains) > 0 {
		networkMode = "allowlist"
	} else if len(reader.deniedDomains) > 0 {
		networkMode = "restricted"
	}
	mcpBoundary := "unavailable"
	if reader.mcpConfigured {
		mcpBoundary = "per-connector"
	}
	return account.RuntimePolicy{
		Source: "local-runtime", ApprovalMode: approvalMode, NetworkMode: networkMode,
		MCPBoundary: mcpBoundary, PermissionProfile: "local-workspace",
	}
}

func (reader *localAccountManagementReader) userByID(accountID string) (synonLinkAuthUser, bool) {
	accountID = strings.TrimSpace(accountID)
	if reader.localAuth != nil && reader.localAuth.enabled && accountID == reader.localAuth.user().ID {
		return reader.localAuth.user(), true
	}
	if reader.accounts == nil {
		return synonLinkAuthUser{}, false
	}
	return reader.accounts.UserByID(accountID)
}

func accountManagementLoginMethods(
	accounts *webAccountStore,
	external *webExternalAuthRuntime,
	localAuth *synonLinkAuthenticator,
	accountID string,
) []account.LoginMethod {
	connected := map[string]bool{}
	if accounts != nil {
		for _, method := range accounts.LoginMethods(accountID) {
			connected[strings.ToLower(strings.TrimSpace(method))] = true
		}
	}
	if localAuth != nil && localAuth.enabled && accountID == localAuth.user().ID {
		connected["local"] = true
	}

	available := map[string]account.LoginMethod{}
	if connected["local"] {
		available["local"] = account.LoginMethod{ID: "local", DisplayName: "Local password", Kind: "password", Enabled: true}
	}
	if external != nil {
		for _, provider := range external.statuses() {
			available[provider.ID] = account.LoginMethod{
				ID: provider.ID, DisplayName: provider.DisplayName, Kind: "external", Enabled: provider.Enabled,
			}
		}
	}
	for method := range connected {
		if _, exists := available[method]; !exists {
			available[method] = account.LoginMethod{ID: method, DisplayName: method, Kind: "external", Connected: true}
		}
	}
	methods := make([]account.LoginMethod, 0, len(available))
	for id, method := range available {
		method.Connected = connected[id]
		methods = append(methods, method)
	}
	sort.Slice(methods, func(i, j int) bool { return methods[i].ID < methods[j].ID })
	return methods
}

func accountDisplayName(user synonLinkAuthUser) string {
	if name := strings.TrimSpace(user.DisplayName); name != "" {
		return name
	}
	return strings.TrimSpace(user.Username)
}
