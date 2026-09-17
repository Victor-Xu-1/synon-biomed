package account

import (
	"context"
	"errors"
)

// ManagementSchemaVersion is the version of the account workbench contract.
// It is independent from the product version so clients can evolve without
// coupling identity data to release packaging.
const ManagementSchemaVersion = 2

type ManagementReadRequest struct {
	AccountID    string
	SessionToken string
}

type ManagementReader interface {
	Read(context.Context, ManagementReadRequest) (ManagementSnapshot, error)
}

type ControlPlane struct {
	Mode                string `json:"mode"`
	Managed             bool   `json:"managed"`
	PolicySource        string `json:"policySource"`
	DataResidency       string `json:"dataResidency"`
	CentralRevocation   bool   `json:"centralRevocation"`
	SessionProtection   bool   `json:"sessionProtection"`
	OfflineWorkAllowed  bool   `json:"offlineWorkAllowed"`
	HighRiskOnlineCheck bool   `json:"highRiskOnlineCheck"`
}

// ControlBoundary makes the control-plane split explicit. A client may show
// these facts, but it must not turn a local status into a server authority.
type ControlBoundary struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	Source           string `json:"source"`
	Managed          bool   `json:"managed"`
	UserConfigurable bool   `json:"userConfigurable"`
}

// RuntimePolicy is the effective local policy used by this client. It is a
// projection, not an authorization token; execution paths remain authoritative
// at their own server/runtime boundaries.
type RuntimePolicy struct {
	Source            string `json:"source"`
	ApprovalMode      string `json:"approvalMode"`
	NetworkMode       string `json:"networkMode"`
	MCPBoundary       string `json:"mcpBoundary"`
	PermissionProfile string `json:"permissionProfile"`
}

type SecurityPosture struct {
	MFAEnabled         bool `json:"mfaEnabled"`
	PasskeyEnabled     bool `json:"passkeyEnabled"`
	RecoveryConfigured bool `json:"recoveryConfigured"`
}

type Account struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	DisplayName   string `json:"displayName"`
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"emailVerified"`
	Provider      string `json:"provider"`
	Status        string `json:"status"`
}

type LoginMethod struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Kind        string `json:"kind"`
	Enabled     bool   `json:"enabled"`
	Connected   bool   `json:"connected"`
}

type Workspace struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Role    string `json:"role"`
	Status  string `json:"status"`
	Current bool   `json:"current"`
}

type Entitlement struct {
	Feature string `json:"feature"`
	Status  string `json:"status"`
	Source  string `json:"source"`
}

type Session struct {
	ID           string `json:"id"`
	AuthMethod   string `json:"authMethod"`
	CreatedAt    string `json:"createdAt"`
	LastSeenAt   string `json:"lastSeenAt"`
	ExpiresAt    string `json:"expiresAt"`
	Remembered   bool   `json:"remembered"`
	UserAgent    string `json:"userAgent"`
	NetworkClass string `json:"networkClass"`
	Current      bool   `json:"current"`
}

type SecurityEvent struct {
	ID           string `json:"id"`
	SessionID    string `json:"sessionId,omitempty"`
	Type         string `json:"type"`
	AuthMethod   string `json:"authMethod,omitempty"`
	CreatedAt    string `json:"createdAt"`
	Success      bool   `json:"success"`
	UserAgent    string `json:"userAgent,omitempty"`
	NetworkClass string `json:"networkClass,omitempty"`
}

type ManagementSnapshot struct {
	SchemaVersion     int               `json:"schemaVersion"`
	ControlPlane      ControlPlane      `json:"controlPlane"`
	ControlBoundaries []ControlBoundary `json:"controlBoundaries"`
	RuntimePolicy     RuntimePolicy     `json:"runtimePolicy"`
	SecurityPosture   SecurityPosture   `json:"securityPosture"`
	Account           Account           `json:"account"`
	LoginMethods      []LoginMethod     `json:"loginMethods"`
	Workspaces        []Workspace       `json:"workspaces"`
	Entitlements      []Entitlement     `json:"entitlements"`
	Sessions          []Session         `json:"sessions"`
	SecurityEvents    []SecurityEvent   `json:"securityEvents"`
}

func (snapshot ManagementSnapshot) Validate() error {
	if snapshot.SchemaVersion != ManagementSchemaVersion {
		return errors.New("unsupported account management schema")
	}
	if snapshot.Account.ID == "" || snapshot.Account.Username == "" {
		return errors.New("account identity is incomplete")
	}
	if snapshot.ControlPlane.Mode == "" || snapshot.ControlPlane.PolicySource == "" ||
		snapshot.ControlPlane.DataResidency == "" {
		return errors.New("account control plane metadata is incomplete")
	}
	if snapshot.ControlBoundaries == nil || snapshot.RuntimePolicy.Source == "" ||
		snapshot.RuntimePolicy.ApprovalMode == "" || snapshot.RuntimePolicy.NetworkMode == "" ||
		snapshot.RuntimePolicy.MCPBoundary == "" || snapshot.RuntimePolicy.PermissionProfile == "" {
		return errors.New("account governance projection is incomplete")
	}
	if snapshot.LoginMethods == nil || snapshot.Workspaces == nil || snapshot.Entitlements == nil ||
		snapshot.Sessions == nil || snapshot.SecurityEvents == nil {
		return errors.New("account management collections must be non-nil")
	}
	return nil
}
