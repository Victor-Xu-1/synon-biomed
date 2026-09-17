package compute

import (
	"errors"
	"strings"
)

const ModalIdentityVersion = "1"

type ModalComputeIdentity struct {
	InstallID   string
	JobID       string
	RootFrameID string
	FrameID     string
	ProjectID   string
}

func ModalComputeIdentityTags(identity ModalComputeIdentity) (map[string]string, error) {
	identity.InstallID = strings.TrimSpace(identity.InstallID)
	identity.JobID = strings.TrimSpace(identity.JobID)
	identity.RootFrameID = strings.TrimSpace(identity.RootFrameID)
	identity.FrameID = strings.TrimSpace(identity.FrameID)
	identity.ProjectID = strings.TrimSpace(identity.ProjectID)
	if identity.InstallID == "" || identity.JobID == "" ||
		identity.RootFrameID == "" || identity.FrameID == "" || identity.ProjectID == "" {
		return nil, errors.New("complete Modal compute identity is required")
	}
	return map[string]string{
		"synonbiomed-org":              identity.InstallID,
		"synonbiomed-session":          identity.RootFrameID,
		"synonbiomed-frame":            identity.FrameID,
		"synonbiomed-job":              identity.JobID,
		"synonbiomed-project":          identity.ProjectID,
		"synonbiomed-identity-version": ModalIdentityVersion,
	}, nil
}
