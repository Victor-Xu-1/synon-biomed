package server

import (
	"strings"

	secretstore "synon-go/internal/persistence/secrets"
)

const (
	imPairingBlockedReason     = "im user is not paired"
	imPairingUnavailableReason = "im pairing store is unavailable"
)

type inboundPairingStatus struct {
	Paired        bool
	Blocked       bool
	BlockedReason string
	OwnerUserID   string
}

func (s *Server) checkInboundPairing(platform string, userID string) (inboundPairingStatus, error) {
	if s == nil || s.pairingStore == nil {
		return inboundPairingStatus{Blocked: true, BlockedReason: imPairingUnavailableReason}, nil
	}
	pairedUser, paired, err := s.pairingStore.Get(platform, userID)
	if err != nil {
		return inboundPairingStatus{}, err
	}
	if !paired {
		return inboundPairingStatus{Blocked: true, BlockedReason: imPairingBlockedReason}, nil
	}
	ownerUserID := strings.TrimSpace(pairedUser.OwnerUserID)
	if ownerUserID == "" && (s.synonLinkAuth == nil || !s.synonLinkAuth.enabled) {
		ownerUserID = secretstore.DefaultUserID
	}
	if ownerUserID == "" {
		return inboundPairingStatus{Blocked: true, BlockedReason: imPairingUnavailableReason}, nil
	}
	return inboundPairingStatus{Paired: true, OwnerUserID: ownerUserID}, nil
}

func applyPairingMap(result map[string]any, pairing inboundPairingStatus) {
	result["paired"] = pairing.Paired
	result["blocked"] = pairing.Blocked
	if pairing.BlockedReason != "" {
		result["blockedReason"] = pairing.BlockedReason
	}
}
